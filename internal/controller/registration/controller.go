// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"flag"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/registration/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	schedulercontrol "github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/ratelimiter"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

const ControllerName = "registration-controller"

var maxConcurrentReconciles = 10
var deprecatedPodWorkers = 1

func init() {
	flag.IntVar(&deprecatedPodWorkers, "pod-workers", 1, "Deprecated: Pod lifecycle uses --registration-workers.")
	flag.IntVar(&maxConcurrentReconciles, "registration-workers", maxConcurrentReconciles, "Max concurrent allocation lifecycle workers.")
}

type Reconciler struct {
	client.Client
	apiReader      client.Reader
	membersIndexed bool
	jobIndex       *slurmjob.PodIndex
	slurmControl   slurmcontrol.SlurmControlInterface
	slurmClient    slurmclient.Client
	informer       slurmclient.InformerCache
	schedulerName  string
	submit         schedulercontrol.JobRegistrar
	registry       *dra.Registry
	workloadAPI    *slurmjobir.WorkloadAPI
	recent         *cache.Expiring
	eventRecorder  record.EventRecorder
	eventHistory   *cache.Expiring
	enqueue        func(Request) // Installed by the source before controller workers start.
}

func NewReconciler(kube client.Client, slurm slurmclient.Client, schedulerName string, registry *dra.Registry, workloadAPI *slurmjobir.WorkloadAPI, mcsLabel, partition string) *Reconciler {
	return &Reconciler{Client: kube, apiReader: kube, slurmControl: slurmcontrol.NewControl(slurm), slurmClient: slurm, informer: slurm.GetInformer(api.ObjectTypeV0044JobInfo), schedulerName: schedulerName,
		submit: schedulercontrol.NewRegistrar(slurm, mcsLabel, partition), registry: registry, workloadAPI: workloadAPI, recent: cache.NewExpiring()}
}

// SetupWithManager serializes allocation registration, publication, and cleanup.
// +kubebuilder:rbac:groups="",resources=pods,verbs=delete;get;list;patch;watch
// +kubebuilder:rbac:groups="",resources=pods/finalizers,verbs=patch;update
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims,verbs=delete;get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets;deployments;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=scheduling.x-k8s.io,resources=podgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=podgroups;workloads;compositepodgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=deviceclasses,verbs=get;list;watch

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.apiReader = mgr.GetAPIReader()
	for name, extract := range map[string]client.IndexerFunc{podGroupMemberIndex: nativePodGroupKeys, publishedJobIndex: publishedJobKeys} {
		if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, name, extract); err != nil {
			return err
		}
	}
	r.membersIndexed = true
	var err error
	r.jobIndex, err = slurmjob.NewPodIndex(r.slurmClient, r.informer)
	if err != nil {
		return err
	}
	r.eventRecorder = mgr.GetEventRecorderFor(ControllerName) //nolint:staticcheck // Record core/v1 Events, matching the other controllers.
	r.eventHistory = cache.NewExpiring()
	jobs := &jobSource{reconciler: r}
	bldr := builder.TypedControllerManagedBy[Request](mgr).Named(ControllerName).
		Watches(&corev1.Pod{}, handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []Request {
			pod := obj.(*corev1.Pod)
			if pod.Spec.SchedulerName != r.schedulerName {
				return nil
			}
			return []Request{podRequest(pod)}
		})).WatchesRawSource(jobs)
	if r.workloadAPI != nil {
		groups := []metav1.TypeMeta{r.workloadAPI.PodGroupTypeMeta}
		composite := schema.GroupVersionKind{Group: "scheduling.k8s.io", Version: "v1alpha3", Kind: "CompositePodGroup"}
		if _, err := mgr.GetRESTMapper().RESTMapping(composite.GroupKind(), composite.Version); err == nil {
			groups = append(groups, metav1.TypeMeta{APIVersion: composite.GroupVersion().String(), Kind: composite.Kind})
		} else if !meta.IsNoMatchError(err) {
			return err
		}
		for _, group := range groups {
			// A recreated native group must wake registration even if its old key
			// was retired. Ownership is resolved during reconciliation, not here.
			bldr.WatchesMetadata(&metav1.PartialObjectMetadata{TypeMeta: group},
				handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []Request {
					return []Request{objectRequest(obj)}
				}))
		}
	}
	return bldr.WithOptions(controller.TypedOptions[Request]{MaxConcurrentReconciles: maxConcurrentReconciles, RateLimiter: ratelimiter.Build[Request]()}).Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, key Request) (reconcile.Result, error) {
	pods, identity, membershipErr := r.members(ctx, key)
	if identity.Index == "" {
		return r.registrationReadEvent(key, membershipErr)
	}
	if synced, err := r.informer.HasSynced(); err != nil || !synced {
		return reconcile.Result{RequeueAfter: registrationRetry}, err
	}
	// Recover conflicts before interpreting submission settings. Invalid current
	// annotations must not prevent cleanup of allocations already in Slurm.
	jobs, err := slurmjob.IndexedAllocation(ctx, r.informer, identity)
	// Deleted Pod and child-group events must still use the root allocation
	// queue; the job's recorded group identity survives Kubernetes deletion.
	for _, job := range jobs {
		owner := job.Ownership().CompositePodGroup
		if owner == nil {
			owner = job.Ownership().PodGroup
		}
		if owner != nil && groupKey(*owner) != key {
			r.enqueue(groupKey(*owner))
			return reconcile.Result{}, nil
		}
	}
	if errors.Is(err, slurmjob.ErrAmbiguousAllocation) {
		// Normalize expected membership waits before joining cleanup errors so
		// missing Pods cannot mask a failed duplicate cancellation.
		_, membershipErr = registrationReadResult(membershipErr)
		return reconcile.Result{RequeueAfter: registrationRetry}, errors.Join(membershipErr, r.cleanDuplicates(ctx, identity, pods))
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	// Lifecycle cleanup must also run for empty or deleted groups and LWS replicas
	// without a leader. Preserve membershipErr to prevent new registration below.
	if membershipErr != nil && !apierrors.IsNotFound(membershipErr) && !errors.Is(membershipErr, slurmjobir.ErrorPodGroupNoPods) && !errors.Is(membershipErr, slurmjobir.ErrorLWSNoPods) && !errors.Is(membershipErr, slurmjob.ErrLWSLeaderMissing) {
		return r.registrationReadEvent(key, membershipErr)
	}
	waiting, err := r.reconcileLifecycle(ctx, key, pods, jobs)
	if err != nil || waiting {
		return reconcile.Result{RequeueAfter: registrationRetry}, err
	}
	if membershipErr != nil {
		return r.registrationReadEvent(key, membershipErr)
	}
	slices.SortFunc(pods, func(a, b corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	for i := range pods {
		pod := &pods[i]
		if !r.eligible(pod) {
			continue
		}
		ir, err := slurmjobir.TranslateToSlurmJobIR(r.Client, r.registry, r.workloadAPI, ctx, pod,
			slurmjobir.WithResourceSlices(slurmjobir.CachedResourceSlices(ctx, r.Client)))
		if err != nil {
			return r.registrationReadEvent(key, err)
		}
		if current := allocationKey(pod, ir.RootPOM); current != key {
			// Only submit under the current allocation key.
			r.enqueue(current)
			continue
		}
		current, err := slurmjob.IdentityForAllocation(pod, ir)
		if err != nil {
			return r.registrationReadEvent(key, err)
		}
		if current.Index != identity.Index || current.Key != identity.Key {
			// The legacy LWS leader changed while membership was read.
			return reconcile.Result{RequeueAfter: registrationRetry}, nil
		}
		return r.register(ctx, key, pod, ir, jobs)
	}

	return reconcile.Result{}, nil
}

// Missing dependencies and incomplete membership are expected while workload
// controllers create objects. Poll them without accumulating failure backoff.
func registrationReadResult(err error) (reconcile.Result, error) {
	if apierrors.IsNotFound(err) || errors.Is(err, slurmjobir.ErrorPodGroupNoPods) ||
		errors.Is(err, slurmjobir.ErrorLWSNoPods) || errors.Is(err, slurmjob.ErrLWSLeaderMissing) {
		return reconcile.Result{RequeueAfter: registrationDependencyRetry}, nil
	}
	return reconcile.Result{}, err
}
