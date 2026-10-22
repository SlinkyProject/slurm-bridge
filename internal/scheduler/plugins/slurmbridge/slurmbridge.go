// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	resourcelisters "k8s.io/client-go/listers/resource/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	lws "sigs.k8s.io/lws/api/leaderworkerset/v1"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmtoken "github.com/SlinkyProject/slurm-client/pkg/client/token"
	"github.com/SlinkyProject/slurm-client/pkg/hostlist"

	"github.com/SlinkyProject/slurm-bridge/internal/config"
	nodecontrollerutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

var (
	ErrorNoKubeNode              = errors.New("no more external nodes available for pod placement")
	ErrorNoKubeNodeMatch         = errors.New("slurm node matches no Kube nodes")
	ErrorPodUpdateFailed         = errors.New("failed to update pod")
	ErrorNodeConfigInvalid       = errors.New("requested node configuration is not available")
	ErrorPodWithResourceClaim    = errors.New("can't schedule pod with a resource claim")
	ErrorPodWithRequiredAffinity = errors.New("can't schedule pod with required affinity: use a Slurm partition or constraint instead")
	ErrorUnprocessedComponents   = errors.New("can't schedule pod: components of SlurmJobIR have not yet passed PostFilter")
)

const slurmJobNotPending = "job is no longer pending execution"

// hasRequiredAffinity reports whether pod has a required (as opposed to
// preferred) node or pod (anti-)affinity term.
func hasRequiredAffinity(pod *corev1.Pod) bool {
	affinity := pod.Spec.Affinity
	if affinity == nil {
		return false
	}
	if na := affinity.NodeAffinity; na != nil {
		if req := na.RequiredDuringSchedulingIgnoredDuringExecution; req != nil && len(req.NodeSelectorTerms) > 0 {
			return true
		}
	}
	if pa := affinity.PodAffinity; pa != nil && len(pa.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
		return true
	}
	if pa := affinity.PodAntiAffinity; pa != nil && len(pa.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
		return true
	}
	return false
}

func findMatchingError(err error, matches func(error) bool) error {
	if err == nil {
		return nil
	}
	if matches(err) {
		return err
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if match := findMatchingError(child, matches); match != nil {
				return match
			}
		}
		return nil
	}

	return findMatchingError(errors.Unwrap(err), matches)
}

func isJobNotPendingError(err error) bool {
	return findMatchingError(err, func(err error) bool {
		msg := strings.ToLower(err.Error())
		return strings.Contains(msg, slurmJobNotPending) ||
			strings.Contains(msg, "eslurm_job_not_pending")
	}) != nil
}

// Scheduler Plugin Core RBAC
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch;update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;patch;watch
// +kubebuilder:rbac:groups="",resources=pods/finalizers,verbs=patch
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch;update
// +kubebuilder:rbac:groups=extensions,resources=replicasets,verbs=get;list;watch

// Delegated Auth RBAC
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create

// RBAC for VolumeBinding Scheduler Plugin
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;update;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;update;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=csidrivers,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=csinodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=csistoragecapacities,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

// RBAC for DefaultBinder Scheduler Plugin
// +kubebuilder:rbac:groups="",resources=pods/binding,verbs=create

// RBAC for nodeinfo.go and dra.go
// +kubebuilder:rbac:groups=resource.k8s.io,resources=deviceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims,verbs=create;get;list;update;watch;delete
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims/binding,verbs=patch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims/status,verbs=patch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceslices,verbs=get;list;watch

// RBAC for Slurm-bridge Workloads
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=compositepodgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=compositepodgroups/status,verbs=patch;update
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=workloads,verbs=get
// +kubebuilder:rbac:groups=scheduling.x-k8s.io,resources=podgroups,verbs=get
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get
// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=get
// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters,verbs=get
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=podgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=podgroups/status,verbs=patch;update

// Slurmbridge is a plugin that schedules pods in a group.
type SlurmBridge struct {
	client.Client
	schedulerName      string
	slurmControl       slurmcontrol.SlurmControlInterface
	handle             fwk.Handle
	draRegistry        *dra.Registry
	workloadAPI        *slurmjobir.WorkloadAPI
	kubeNodeIndex      *kubeNodeNameIndex
	allocationObserver *allocationObserver
	mu                 sync.Mutex
	pending            map[groupAllocation]*groupProgress

	resourceSlices resourcelisters.ResourceSliceLister
}

type groupAllocation struct {
	uid   types.UID
	jobID int32
}

// pendingTimeout drops hetjob progress no component has touched recently.
const pendingTimeout = 5 * time.Minute

type groupProgress struct {
	components map[types.NamespacedName][]string
	held       bool
	updating   bool
	updated    time.Time
}

func (p *groupProgress) recordComponent(ir *slurmjobir.SlurmJobIR, index int) bool {
	component := &ir.Components[index]
	p.components[component.GetNamespacedName()] = slices.Clone(component.JobInfo.ExcNodes)

	for i := range ir.Components {
		component = &ir.Components[i]
		excluded, processed := p.components[component.GetNamespacedName()]
		if !processed {
			return false
		}
		if len(excluded) > 0 && len(component.JobInfo.ExcNodes) == 0 {
			component.JobInfo.ExcNodes = excluded
		}
	}
	return true
}

var _ fwk.PreFilterPlugin = &SlurmBridge{}
var _ fwk.FilterPlugin = &SlurmBridge{}
var _ fwk.PostFilterPlugin = &SlurmBridge{}
var _ fwk.PodGroupPostFilterPlugin = &SlurmBridge{}
var _ fwk.PreBindPlugin = &SlurmBridge{}

const (
	Name                  = "SlurmBridge"
	stateKey fwk.StateKey = Name
)

// ConfigFile is the path to the slurm-bridge config file read by New, overridable via
// cmd/scheduler/main.go's "--slurm-bridge-config" flag.
var ConfigFile = config.ConfigFile

// Name returns name of the plugin. It is used in logs, etc.
func (sb *SlurmBridge) Name() string {
	return Name
}

type stateData struct {
	slurmJobIR *slurmjobir.SlurmJobIR
	allocation []slurmcontrol.ExternalJob
	native     *nativeAllocation
	nodeName   string
}

func (d *stateData) Clone() fwk.StateData {
	copy := *d
	return &copy
}

func getStateData(cs fwk.CycleState) (*stateData, error) {
	state, err := cs.Read(stateKey)
	if err != nil {
		return nil, err
	}
	s, ok := state.(*stateData)
	if !ok {
		return nil, errors.New("unable to convert state into stateData")
	}
	return s, nil
}

// componentForPod returns the SlurmJobComponent for pod, or an error status
// if pod has no known component in the job IR.
func (s *stateData) componentForPod(pod *corev1.Pod) (*slurmjobir.SlurmJobComponent, *fwk.Status) {
	componentIndex := s.slurmJobIR.ComponentOf(pod.Namespace, pod.Name)
	if componentIndex == -1 {
		return nil, fwk.NewStatus(fwk.Error, fmt.Sprintf("Invalid component index %v for pod %v", componentIndex, klog.KObj(pod)))
	}
	return &s.slurmJobIR.Components[componentIndex], nil
}

// activatePod will put the pod back into the scheduling queue.
func (sb *SlurmBridge) activatePod(logger klog.Logger, pod *corev1.Pod) {
	sb.handle.Activate(logger, map[string]*corev1.Pod{string(pod.UID): pod})
}

func newClientScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for name, addToScheme := range map[string]func(*runtime.Scheme) error{
		"metadata":          metav1.AddMetaToScheme,
		"core":              corev1.AddToScheme,
		"batch":             batchv1.AddToScheme,
		"resource":          resourcev1.AddToScheme,
		"scheduler-plugins": sched.AddToScheme,
		"jobset":            jobset.AddToScheme,
		"leader-worker-set": lws.AddToScheme,
	} {
		if err := addToScheme(scheme); err != nil {
			return nil, fmt.Errorf("register %s API scheme: %w", name, err)
		}
	}
	return scheme, nil
}

// New initializes and returns a new Slurmbridge plugin.
func New(ctx context.Context, obj runtime.Object, handle fwk.Handle) (fwk.Plugin, error) {

	logger := klog.FromContext(ctx)
	logger.V(5).Info("creating new SlurmBridge plugin")

	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		logger.Error(err, "unable to read config file", "file", ConfigFile)
		return nil, err
	}
	cfg, err := config.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	if err := cfg.ValidateScheduler(); err != nil {
		return nil, err
	}
	draRegistry, err := cfg.DRARegistry()
	if err != nil {
		return nil, fmt.Errorf("configure DRA device profiles: %w", err)
	}

	clientScheme, err := newClientScheme()
	if err != nil {
		return nil, err
	}
	// handle.KubeConfig() is the plain input config, not QPS/Burst-tuned, so
	// this plugin's own client needs its own (user-configurable) tuning.
	restConfig := rest.CopyConfig(handle.KubeConfig())
	restConfig.QPS, restConfig.Burst = cfg.EffectiveClientQPSBurst()
	workloadAPI, err := slurmjobir.NewWorkloadAPI(restConfig, clientScheme)
	if err != nil {
		return nil, err
	}
	if workloadAPI != nil {
		logger.Info("registered built-in Workload API", "apiVersion", workloadAPI.PodGroupTypeMeta.APIVersion)
	} else {
		logger.Info("built-in Workload support disabled", "featureGate", features.GenericWorkload)
	}

	// The Pod client uses the same QPS/Burst tuning as the Workload API client.
	kubeClient, err := newKubeClient(restConfig, clientScheme)
	if err != nil {
		return nil, err
	}
	clientConfig := &slurmclient.Config{
		Server:        cfg.SlurmRestApi,
		TokenProvider: slurmtoken.FileProvider{Path: os.Getenv("SLURM_JWT_FILE")},
		HTTPClient:    &http.Client{Timeout: config.SlurmClientTimeout},
	}
	slurmClient, err := slurmclient.NewClient(clientConfig, &slurmclient.ClientOptions{CacheSyncPeriod: config.SlurmCacheSyncPeriod})
	if err != nil {
		logger.Error(err, "unable to create slurm client")
		return nil, err
	}
	plugin := &SlurmBridge{
		Client:        kubeClient,
		schedulerName: cfg.SchedulerName,
		handle:        handle,
		draRegistry:   draRegistry,
		workloadAPI:   workloadAPI,
	}
	plugin.resourceSlices = handle.SharedInformerFactory().Resource().V1().ResourceSlices().Lister()
	plugin.kubeNodeIndex, err = newKubeNodeNameIndex(handle.SharedInformerFactory().Core().V1().Nodes().Informer())
	if err != nil {
		return nil, err
	}
	if _, err := handle.SharedInformerFactory().Core().V1().Pods().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: plugin.recordRegistrationPending,
	}); err != nil {
		return nil, fmt.Errorf("register initial Pod diagnostics: %w", err)
	}
	plugin.allocationObserver, err = newAllocationObserver(ctx, plugin, clientConfig)
	if err != nil {
		return nil, err
	}
	// Read the same job cache that promotes ready allocations. A separate,
	// slower job informer could otherwise reject a freshly promoted workload.
	plugin.slurmControl, err = slurmcontrol.NewControl(slurmClient, plugin.allocationObserver.jobs, cfg.MCSLabel, cfg.Partition)
	if err != nil {
		return nil, err
	}
	nodeInformer := slurmClient.GetInformer(api.ObjectTypeV0044Node)
	go nodeInformer.Run(ctx.Done())
	if err := wait.PollUntilContextCancel(ctx, time.Millisecond, true, func(context.Context) (bool, error) {
		return nodeInformer.HasStarted(), nil
	}); err != nil {
		return nil, err
	}
	// Prime the index without waiting for the informer's first periodic refresh.
	go func() {
		if err := nodeInformer.List(ctx, &api.V0044NodeObjectList{}, &slurmclient.ListOptions{RefreshCache: true}); err != nil && ctx.Err() == nil {
			logger.Error(err, "unable to initialize Slurm node index")
		}
	}()
	return plugin, nil
}

// PreFilter checks the allocation registered by the controller.
// Pods without an allocation wait for registration.
// If an external job is found, determine which node(s) have been assigned to the
// Slurm job and update state so the Filter plugin can filter out the assigned node(s)
func (sb *SlurmBridge) PreFilter(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, nodeInfo []fwk.NodeInfo) (*fwk.PreFilterResult, *fwk.Status) {
	result, status := sb.preFilterPod(ctx, state, pod, nodeInfo)
	for groupState := state.GetPodGroupSchedulingCycle(); groupState != nil; {
		var group *nativeGroupCycle
		if data, err := groupState.Read(nativeGroupCycleKey); err == nil {
			group = data.(*nativeGroupCycle)
		} else {
			group = &nativeGroupCycle{}
			groupState.Write(nativeGroupCycleKey, group)
		}
		group.members = append(group.members, nativeGroupMemberCycle{state: state, pod: pod, result: result, status: status})

		parentPlacement := groupState.GetParentPlacementCycleState()
		if parentPlacement == nil {
			break
		}
		groupState = parentPlacement.GetPodGroupSchedulingCycle()
	}
	return result, status
}

func (sb *SlurmBridge) preFilterPod(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, nodeInfo []fwk.NodeInfo) (*fwk.PreFilterResult, *fwk.Status) {
	if sb.allocationObserver != nil {
		// Start only once this replica is scheduling; standby replicas must not patch hints.
		sb.allocationObserver.start()
	}
	logger := klog.FromContext(ctx)
	var err error

	if err := slurmjobir.ValidatePodGroupSupport(sb.workloadAPI, pod); err != nil {
		return nil, fwk.NewStatus(fwk.UnschedulableAndUnresolvable, err.Error())
	}

	if pod.Spec.ResourceClaims != nil {
		logger.Error(ErrorPodWithResourceClaim, "use extended resource or device plugin request instead")
		return nil, fwk.NewStatus(fwk.Unschedulable, ErrorPodWithResourceClaim.Error())
	}

	// Required affinity is a hard constraint; honoring required NodeAffinity would
	// mean growing the excluded-node list slurm-bridge already sends Slurm per pod
	// (see #132: that list's churn is a known scheduling performance cost we're
	// trying to reduce, not add to), and required PodAffinity/PodAntiAffinity would
	// be silently violated today since InterPodAffinity never runs in this
	// scheduler's profile. Reject rather than silently violate it; also enforced at
	// admission for pods that bypass this scheduler. Use a Slurm partition or
	// constraint instead.
	//
	// Preferred affinity is only a scoring hint, even upstream kube-scheduler
	// treats it as best-effort, so it's fine to silently not act on it.
	if hasRequiredAffinity(pod) {
		logger.Error(ErrorPodWithRequiredAffinity, "required affinity is not supported")
		return nil, fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodWithRequiredAffinity.Error())
	}

	if group := nativeSchedulingCycle(state, pod); group != nil {
		return sb.preFilterNative(ctx, state, pod, group)
	}

	s := &stateData{}
	state.Write(stateKey, s)

	// Construct an intermediate representation of the Slurm external job
	s.slurmJobIR, err = slurmjobir.TranslateToSlurmJobIR(sb.Client, sb.registry(), sb.workloadAPI, ctx, pod,
		slurmjobir.WithResourceSlices(sb.resourceSlices))
	if errors.Is(err, slurmjobir.ErrorPodGroupUnsupported) {
		return nil, fwk.NewStatus(fwk.UnschedulableAndUnresolvable, err.Error())
	}
	if err != nil {
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}
	root := &s.slurmJobIR.RootPOM
	rootName := root.Name
	if root.Namespace != "" {
		rootName = root.Namespace + "/" + root.Name
	}
	logger.V(3).Info("selected workload root",
		"pod", klog.KObj(pod),
		"apiVersion", root.APIVersion,
		"kind", root.Kind,
		"root", rootName)
	for _, component := range s.slurmJobIR.Components {
		if err := sb.validateDeviceClassRequestsForPods(ctx, component.Pods.Items, ptr.Deref(component.JobInfo.Exclusive, true)); err != nil {
			logger.Error(err, "unsupported DRA extended resource request")
			return nil, fwk.NewStatus(fwk.UnschedulableAndUnresolvable, err.Error())
		}
	}

	jobs, allocationErr := sb.slurmControl.GetAllocation(ctx, pod, s.slurmJobIR)
	allocationStatus := s.checkAllocation(pod, jobs, allocationErr)
	if !allocationStatus.IsSuccess() {
		// Only a missing or finished allocation may retry stale metadata cleanup.
		// Active conflicts and lookup failures must wait for registration.
		inactive := errors.Is(allocationErr, slurmjob.ErrAllocationMissing) ||
			(allocationErr == nil && !slices.ContainsFunc(jobs, func(job slurmcontrol.ExternalJob) bool { return !job.Finished }))
		if !inactive {
			s.slurmJobIR = nil
			return nil, allocationStatus
		}
	}

	// Retry failed metadata cleanup for a deleted node even after the canceled
	// allocation disappears from Slurm, before waiting for registration.
	node := pod.Annotations[wellknown.AnnotationExternalJobNode]
	jobID := pod.Labels[wellknown.LabelExternalJobId]
	if jobID != "" && node != "" {
		if err := sb.Get(ctx, client.ObjectKey{Name: node}, &corev1.Node{}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, sb.handleAllocatedNodeError(ctx, s, pod, ErrorNoKubeNodeMatch)
			}
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
	}
	if !allocationStatus.IsSuccess() {
		s.slurmJobIR = nil
		return nil, allocationStatus
	}

	if jobID != "" && node != "" {
		component, status := s.componentForPod(pod)
		if status != nil {
			return nil, status
		}
		sb.markPodGroupScheduled(ctx, s.slurmJobIR, component, jobID)
		phNode := make(sets.Set[string])
		phNode.Insert(node)
		return &fwk.PreFilterResult{NodeNames: phNode}, fwk.NewStatus(fwk.Success)
	}

	externalJob := jobs[s.slurmJobIR.ComponentOf(pod.Namespace, pod.Name)]

	// Perform resource specific PreFilter
	fs := slurmjobir.PreFilter(sb.Client, sb.registry(), sb.handle, sb.workloadAPI, ctx, pod, s.slurmJobIR)
	if fs.Code() != fwk.Success {
		// An Unschedulable status still reaches PostFilter; do not release an invalid workload.
		s.slurmJobIR = nil
		// If the external job is determined to no longer be valid
		// delete the external job and remove the associated annotations
		for _, r := range fs.Reasons() {
			if r == slurmjobir.ErrorExternalJobInvalid.Error() {
				logger.Error(err, "external job no longer valid, deleting job")
				err := sb.deleteExternalJob(ctx, pod)
				if err != nil {
					return nil, fwk.NewStatus(fwk.Error, err.Error())
				}
			}
		}
		return nil, fs
	}

	// If no external job exists, or the external job exists but Slurm has not
	// assigned nodes yet, return success with no PreFilterResult. Filter will
	// detect the missing node annotation and PostFilter will update
	// the external job. If the external job has nodes, annotate the pods so
	// scheduling can continue against the Slurm allocation.
	if externalJob.JobId == 0 {
		return nil, fwk.NewStatus(fwk.Success)
	} else {
		logger.V(4).Info("external job exists")
		if externalJob.Nodes == "" {
			logger.V(4).Info("external job exists but no nodes have been allocated")
			return nil, fwk.NewStatus(fwk.Success)
		}
		// The external job is running. Assign nodes to pods.
		slurmNodes, _ := hostlist.Expand(externalJob.Nodes)
		kubeNodes, err := sb.slurmToKubeNodes(ctx, slurmNodes)
		if err != nil {
			if errors.Is(err, ErrorNoKubeNodeMatch) {
				if status := sb.confirmPlacement(ctx, pod, externalJob); !status.IsSuccess() {
					return nil, status
				}
			}
			return nil, sb.handleAllocatedNodeError(ctx, s, pod, err)
		}

		component, status := s.componentForPod(pod)
		if status != nil {
			return nil, status
		}

		err = sb.annotatePodsWithNodes(ctx, externalJob.JobId, kubeNodes.Clone(), &component.Pods)
		if err != nil {
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		sb.markPodGroupScheduled(ctx, s.slurmJobIR, component, strconv.Itoa(int(externalJob.JobId)))
		// Read refreshed metadata into a private object. Informer and framework
		// Pods are shared with the allocation observer and must stay immutable.
		refreshed := &corev1.Pod{}
		if err := sb.Get(ctx, client.ObjectKeyFromObject(pod), refreshed); err != nil {
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		if refreshed.UID != pod.UID {
			return nil, fwk.NewStatus(fwk.Unschedulable, "Pod identity changed during placement")
		}
		s.nodeName = refreshed.Annotations[wellknown.AnnotationExternalJobNode]
		// By passing the list of nodes in the external job as PreFilterResult,
		// Filter plugins will only run for nodes in the Slurm job. This is the final
		// PreFilter step that must occur before pods are allowed to run.
		return &fwk.PreFilterResult{NodeNames: kubeNodes}, fwk.NewStatus(fwk.Success, "")
	}
}

// handleAllocatedNodeError releases an allocation only after a node lookup
// confirms that an allocated node has no Kubernetes counterpart.
func (sb *SlurmBridge) handleAllocatedNodeError(ctx context.Context, s *stateData, pod *corev1.Pod, err error) *fwk.Status {
	if !errors.Is(err, ErrorNoKubeNodeMatch) {
		return fwk.NewStatus(fwk.Error, err.Error())
	}
	logger := klog.FromContext(ctx)
	logger.Info("Slurm allocation includes a missing Kubernetes node, deleting external job for retry",
		"pod", klog.KObj(pod), "jobId", pod.Labels[wellknown.LabelExternalJobId])
	if err := sb.deleteExternalJob(ctx, pod); err != nil {
		return fwk.NewStatus(fwk.Error, err.Error())
	}
	// PostFilter may run after a failed PreFilter. It must wait for a new
	// scheduling cycle to compute feasible nodes without the old allocation.
	s.slurmJobIR = nil
	toActivate := pod.DeepCopy()
	delete(toActivate.Labels, wellknown.LabelExternalJobId)
	delete(toActivate.Annotations, wellknown.AnnotationExternalJobNode)
	sb.activatePod(logger, toActivate)
	return fwk.NewStatus(fwk.Unschedulable, ErrorNoKubeNodeMatch.Error())
}

// allocatedNodeRejectedByKubernetes returns true only when PreFilter observed a
// Slurm allocation and a Kubernetes Filter plugin rejected the node assigned to
// this pod. Without a placement decision, Slurm may have allocated the job after
// PreFilter ran; that valid allocation must be preserved for the next cycle.
func allocatedNodeRejectedByKubernetes(assignedNode string, externalJob *slurmcontrol.ExternalJob, m fwk.NodeToStatusReader) bool {
	if externalJob.JobId == 0 || externalJob.Nodes == "" {
		return false
	}
	if assignedNode == "" {
		return false
	}
	status := m.Get(assignedNode)
	return status.Code() != fwk.Success && status.Plugin() != "" && status.Plugin() != Name
}

// PostFilter updates feasible nodes and releases controller-registered jobs.
// Every hetjob component must pass Kubernetes feasibility checks before release.
func (sb *SlurmBridge) PostFilter(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, m fwk.NodeToStatusReader) (*fwk.PostFilterResult, *fwk.Status) {
	logger := klog.FromContext(ctx)

	s, err := getStateData(state)
	if err != nil {
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}
	// PreFilter rejected the workload, so there is nothing to update or release.
	if s.slurmJobIR == nil {
		return nil, fwk.NewStatus(fwk.Unschedulable)
	}
	// Eligibility updates are private to this member; shared membership and
	// allocation snapshots remain immutable through concurrent binding.
	s.slurmJobIR = copySchedulingIR(s.slurmJobIR)
	assignedNode := s.assignedNode(pod)
	jobs, allocationErr := sb.slurmControl.GetAllocation(ctx, pod, s.slurmJobIR)
	if status := s.checkAllocation(pod, jobs, allocationErr); !status.IsSuccess() {
		return nil, status
	}

	externalJob := jobs[s.slurmJobIR.ComponentOf(pod.Namespace, pod.Name)]
	if allocatedNodeRejectedByKubernetes(assignedNode, &externalJob, m) {
		// TODO: When Slurm supports external-job requeue, confirm the rejected
		// placement: a refresh may show new nodes under the same job ID while
		// the Pod annotation and Filter result still refer to the old node.
		if status := sb.confirmPlacement(ctx, pod, externalJob); !status.IsSuccess() {
			return nil, status
		}
		waiting, status := sb.allocatedNodeWaitingForBridge(ctx, state, pod, assignedNode, m)
		if !status.IsSuccess() {
			return nil, status
		}
		if waiting {
			logger.V(4).Info("Waiting for previous Bridge allocation to release Kubernetes resources",
				"pod", klog.KObj(pod), "jobId", externalJob.JobId)
			// NodeResourcesFit's queueing events retry the pod when resources
			// change. Retain the allocation and all gang members in the meantime.
			// TODO: Bound this wait and recover if terminating pods or pods from
			// finished jobs never leave the node; stalled cleanup can otherwise
			// hold this gang's Slurm allocation indefinitely.
			return nil, fwk.NewStatus(fwk.Unschedulable, "waiting for previous Bridge allocation to release resources")
		}
		logger.Info("Slurm allocation rejected by Kubernetes, deleting external job for retry",
			"pod", klog.KObj(pod), "jobId", externalJob.JobId, "nodes", externalJob.Nodes)
		if err := sb.deleteExternalJob(ctx, pod); err != nil {
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		sb.activatePod(logger, pod)
		return nil, fwk.NewStatus(fwk.Success)
	}

	if externalJob.JobId == 0 {
		return nil, fwk.NewStatus(fwk.Unschedulable, "waiting for Slurm registration")
	}

	// populate the pod's SlurmJobComponent with eligible Slurm node names based
	// on nodes that have passed Filter plugins
	if status := sb.populateComponentWithFeasibleNodes(ctx, state, s, pod, m); status != nil {
		return nil, status
	}

	if s.slurmJobIR.IsHetJob() && externalJob.Pending {
		sb.mu.Lock()
		allocation := groupAllocation{uid: s.slurmJobIR.RootPOM.UID, jobID: s.allocation[0].AllocationID()}

		if sb.pending == nil {
			sb.pending = make(map[groupAllocation]*groupProgress)
		}
		for uid, p := range sb.pending {
			if !p.updating && time.Since(p.updated) > pendingTimeout {
				delete(sb.pending, uid)
			}
		}
		progress := sb.pending[allocation]
		if progress == nil {
			progress = &groupProgress{components: make(map[types.NamespacedName][]string)}
			sb.pending[allocation] = progress
		}
		progress.updated = time.Now()
		progress.held = progress.held || externalJob.Held

		index := s.slurmJobIR.ComponentOf(pod.Namespace, pod.Name)
		if !progress.recordComponent(s.slurmJobIR, index) {
			sb.mu.Unlock()
			logger.V(4).Info("skipping Slurm hold release for workload with unprocessed components", "pod", klog.KObj(pod))
			return nil, fwk.AsStatus(ErrorUnprocessedComponents)
		}

		if progress.updating {
			logger.V(4).Info("skipping concurrent eligibility update", "pod", klog.KObj(pod))
			sb.mu.Unlock()
			return nil, fwk.NewStatus(fwk.Success)

		}

		progress.updating = true
		externalJob.Held = progress.held

		defer func() {
			sb.mu.Lock()
			progress.updating = false
			sb.mu.Unlock()
		}()

		sb.mu.Unlock()
	}

	if externalJob.Pending {
		if status := sb.updateExternalJob(ctx, s, externalJob.Held); !status.IsSuccess() {
			return nil, status
		}
	}

	sb.mu.Lock()
	delete(sb.pending, groupAllocation{uid: s.slurmJobIR.RootPOM.UID, jobID: s.allocation[0].AllocationID()})
	sb.mu.Unlock()

	// Successful PostFilter ends this attempt; it does not proceed to binding.
	// Writes have been acknowledged without reading back the updated jobs, and
	// eligibility is updated, but the job may still be pending in Slurm.
	// The allocation observer activates the workload when its allocation arrives.
	// Otherwise retain normal backoff, including for an unchanged ready allocation.
	return nil, fwk.NewStatus(fwk.Success, "")
}

const nativeGroupCycleKey fwk.StateKey = Name + "/native-group"

// A native group cycle runs scheduling hooks serially. Allocation outcomes are
// discarded with the cycle rather than surviving a retry.
// Each member's ordinary state retains the immutable allocation for binding,
// after the framework detaches the PodGroup scheduling-cycle state.
type nativeGroupCycle struct {
	completed   map[types.UID]bool
	members     []nativeGroupMemberCycle
	allocations map[client.ObjectKey]*nativeAllocation
}

type nativeGroupMemberCycle struct {
	state  fwk.CycleState
	pod    *corev1.Pod
	result *fwk.PreFilterResult
	status *fwk.Status
}

func (s *nativeGroupCycle) Clone() fwk.StateData { return s }

func (sb *SlurmBridge) PodGroupPostFilter(ctx context.Context, state fwk.PodGroupCycleState, pgInfo fwk.PodGroupInfo, _ fwk.PodGroupSchedulingFunc) (*fwk.PodGroupPostFilterResult, *fwk.Status) {
	data, err := state.Read(nativeGroupCycleKey)
	if err != nil && pgInfo.GetType() != fwk.CompositePodGroupKeyType {
		return nil, fwk.NewStatus(fwk.Unschedulable, "native PodGroup has no scheduling decision")
	}
	group := &nativeGroupCycle{}
	if err == nil {
		group = data.(*nativeGroupCycle)
	} else {
		state.Write(nativeGroupCycleKey, group)
	}
	evaluated := make(map[types.UID]bool, len(group.members))
	for _, member := range group.members {
		if member.status.IsError() {
			return nil, member.status
		}
		evaluated[member.pod.UID] = true
	}
	var unevaluated []*corev1.Pod
	for _, pod := range pgInfo.GetUnscheduledPods() {
		if !evaluated[pod.UID] {
			unevaluated = append(unevaluated, pod)
		}
	}
	klog.FromContext(ctx).V(4).Info("found unevaluated PodGroup members", "count", len(unevaluated))
	if pgInfo.GetType() == fwk.CompositePodGroupKeyType && len(unevaluated) > 0 {
		runner, ok := sb.handle.(framework.Framework)
		if !ok {
			return nil, fwk.NewStatus(fwk.Error, "scheduler framework cannot run PreFilter plugins")
		}
		for _, pod := range unevaluated {
			podState := framework.NewCycleState()
			podState.SetPodGroupSchedulingCycle(state)
			previousMembers := len(group.members)
			result, status, _ := runner.RunPreFilterPlugins(ctx, podState, pod)
			if status.IsError() {
				return nil, status
			}
			if len(group.members) == previousMembers {
				group.members = append(group.members, nativeGroupMemberCycle{state: podState, pod: pod, result: result, status: status})
			}
		}
	}
	allocations := make(map[types.UID]types.UID)
	pending := make(map[types.UID]bool)
	// Translation defines both the root and complete membership of an allocation.
	// Index each allocation once, including members rejected before translation.
	for _, member := range group.members {
		s, err := getStateData(member.state)
		if err != nil {
			if member.status.IsSuccess() {
				return nil, fwk.AsStatus(err)
			}
			continue
		}
		if s.native != nil {
			// Replaced or late members can fail before receiving a valid IR.
			// They still block the allocation whose snapshot rejected them.
			allocations[member.pod.UID] = s.native.ir.RootPOM.UID
		}
		if s.slurmJobIR == nil {
			continue
		}
		root := s.slurmJobIR.RootPOM.UID
		if _, found := pending[root]; !found {
			for _, component := range s.slurmJobIR.Components {
				for _, pod := range component.Pods.Items {
					allocations[pod.UID] = root
				}
			}
		}
		pending[root] = pending[root] || member.status.IsSuccess() && member.result == nil
	}
	blocked := make(map[types.UID]bool)
	var failure *fwk.Status
	for _, member := range group.members {
		if member.status.IsSuccess() {
			continue
		}
		if allocation, found := allocations[member.pod.UID]; found {
			blocked[allocation] = true
		}
		failure = member.status
	}
	nodes, err := sb.handle.SnapshotSharedLister().NodeInfos().List()
	if err != nil {
		return nil, fwk.AsStatus(err)
	}
	if group.completed == nil {
		group.completed = make(map[types.UID]bool)
	}
	completed := group.completed
	for _, member := range group.members {
		allocation, found := allocations[member.pod.UID]
		if !found || blocked[allocation] || completed[allocation] {
			continue
		}
		statuses := framework.NewDefaultNodeToStatus()
		failed := false
		for _, node := range nodes {
			if node.Node() == nil {
				continue
			}
			name := node.Node().Name
			if member.result != nil && !member.result.AllNodes() && !member.result.NodeNames.Has(name) {
				continue
			}
			status := sb.handle.RunFilterPluginsWithNominatedPods(ctx, member.state, member.pod, node)
			if status.Code() == fwk.Error {
				return nil, status
			}
			statuses.Set(name, status)
			failed = failed || !status.IsSuccess()
		}
		if pending[allocation] || failed {
			_, status := sb.PostFilter(ctx, member.state, member.pod, statuses)
			if status.IsSuccess() {
				completed[allocation] = true
				continue
			}
			if !errors.Is(status.AsError(), ErrorUnprocessedComponents) {
				if status.IsError() {
					return nil, status
				}
				blocked[allocation] = true
				failure = status
			}
		}
	}
	if len(completed) > 0 {
		return &fwk.PodGroupPostFilterResult{}, nil
	}
	if failure != nil {
		return nil, failure
	}
	return nil, fwk.AsStatus(ErrorUnprocessedComponents)
}

// populateComponentWithFeasibleNodes records eligible Slurm node names for the
// pod's component. Resources occupied by Bridge allocations remain eligible for
// Slurm's pending queue; other Kubernetes constraints still exclude the node.
func (sb *SlurmBridge) populateComponentWithFeasibleNodes(ctx context.Context, state fwk.CycleState, s *stateData, pod *corev1.Pod, m fwk.NodeToStatusReader) *fwk.Status {
	logger := klog.FromContext(ctx)

	feasibleNodes, err := sb.handle.SnapshotSharedLister().NodeInfos().List()
	if err != nil {
		logger.Error(err, "error getting nodes that SlurmBridge can use")
		return fwk.NewStatus(fwk.Error, err.Error())
	}

	component, status := s.componentForPod(pod)
	if status != nil {
		return status
	}

	slurmNodeNames, err := sb.slurmControl.GetNodeNames(ctx, component.JobInfo.Partition)
	if err != nil {
		logger.Error(err, "error getting Slurm nodes")
		return fwk.NewStatus(fwk.Error, err.Error())
	}
	slurmNodes := set.New(slurmNodeNames...)
	feasibleSlurmNodes := set.New[string]()
	for _, node := range feasibleNodes {
		if node.Node() == nil {
			continue
		}
		slurmName := nodecontrollerutils.GetSlurmNodeName(node.Node())
		if !slurmNodes.Has(slurmName) {
			continue
		}
		status := m.Get(node.Node().Name)
		eligible, eligibilityStatus := sb.nodeEligibleForSlurm(ctx, state, pod, node, status, nil)
		if !eligibilityStatus.IsSuccess() {
			return eligibilityStatus
		}
		if eligible {
			feasibleSlurmNodes.Insert(slurmName)
		}
	}

	// A gang still needs enough eligible nodes, but they need not be free now.
	if len(feasibleSlurmNodes) < len(component.Pods.Items) {
		return fwk.NewStatus(fwk.Unschedulable, "insufficient eligible nodes for allocation")
	}

	component.JobInfo.ExcNodes = slurmNodes.Difference(feasibleSlurmNodes).UnsortedList()
	slices.Sort(component.JobInfo.ExcNodes)

	return nil
}

// Update all components before releasing any hold. Repeating this after a
// partial failure is safe; Slurm rejects updates once the allocation starts.
func (sb *SlurmBridge) updateExternalJob(ctx context.Context, s *stateData, held bool) *fwk.Status {
	if status := sb.checkAllocation(ctx, s, &s.slurmJobIR.Components[0].Pods.Items[0]); !status.IsSuccess() {
		return status
	}
	for i := range s.slurmJobIR.Components {
		component := &s.slurmJobIR.Components[i]
		pod := &component.Pods.Items[0]
		priority := component.JobInfo.Priority
		// An explicit priority could release a held component before its siblings
		// are ready. Apply it only after all eligibility updates have succeeded.
		if held {
			component.JobInfo.Priority = nil
		}
		_, err := sb.slurmControl.UpdateJob(ctx, pod, s.slurmJobIR)
		component.JobInfo.Priority = priority
		if err != nil {
			if isJobNotPendingError(err) {
				return fwk.NewStatus(fwk.Unschedulable, "allocation changed while updating eligibility")
			}
			return fwk.AsStatus(err)
		}
	}
	if !held {
		return nil
	}
	for i := range s.slurmJobIR.Components {
		component := &s.slurmJobIR.Components[i]
		if status := sb.checkAllocation(ctx, s, &component.Pods.Items[0]); !status.IsSuccess() {
			return status
		}
		id := s.allocation[i].JobId
		if err := sb.slurmControl.ReleaseJob(ctx, id, component.JobInfo.Priority); err != nil {
			return fwk.AsStatus(err)
		}
	}
	return nil
}

// PreBindPreFlight will check if any GRES was requested for the external job
func (sb *SlurmBridge) PreBindPreFlight(ctx context.Context, cs fwk.CycleState, pod *corev1.Pod, nodeName string) (*fwk.PreBindPreFlightResult, *fwk.Status) {
	return nil, nil
}

// PreBind will generate ResourceClaims for any GRES allocation in Slurm.
// If a GRES allocation does not have a corresponding DeviceClass, it will
// be skipped.
func (sb *SlurmBridge) PreBind(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, nodeName string) *fwk.Status {
	s, err := getStateData(state)
	if err != nil {
		return fwk.AsStatus(err)
	}
	if len(s.allocation) == 0 {
		return fwk.NewStatus(fwk.Unschedulable, "waiting for Slurm registration")
	}
	if s.native == nil {
		if status := sb.checkAllocation(ctx, s, pod); !status.IsSuccess() {
			return status
		}
	}

	// Note that whole node allocations in slurm will look like all
	// resources were requested, but that doesn't mean the pod
	// intended to use them.
	node := &corev1.Node{}
	if err := sb.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fwk.NewStatus(fwk.Error, err.Error())
	}
	slurmNode := nodecontrollerutils.GetSlurmNodeName(node)
	var resources *slurmcontrol.NodeResources
	if s.native != nil {
		if s.nodeName != nodeName {
			return fwk.NewStatus(fwk.Unschedulable, "selected node differs from gang assignment")
		}
		var status *fwk.Status
		resources, status = s.native.resourcesFor(ctx, sb.slurmControl, pod, slurmNode)
		if !status.IsSuccess() {
			return status
		}
	} else {
		// Discovery uses cached state, so validate the selected placement here in
		// concurrent binding before creating claims or binding a Pod.
		liveJob, err := sb.slurmControl.GetJob(ctx, pod)
		if err != nil {
			return fwk.AsStatus(err)
		}
		expected := s.allocation[s.slurmJobIR.ComponentOf(pod.Namespace, pod.Name)]
		slurmNodes, err := hostlist.Expand(liveJob.Nodes)
		if err != nil {
			return fwk.AsStatus(err)
		}
		if liveJob.JobId != expected.JobId || liveJob.HetJobId != expected.HetJobId ||
			liveJob.Pending || liveJob.Finished || !slices.Contains(slurmNodes, slurmNode) {
			return fwk.NewStatus(fwk.Unschedulable, "Slurm allocation no longer owns the selected node")
		}
		resources, err = sb.slurmControl.GetResources(ctx, pod, slurmNode)
	}
	if err != nil {
		return fwk.NewStatus(fwk.Error, err.Error())
	}

	err = sb.manageResourceClaim(ctx, pod, nodeName, resources)
	if err != nil {
		return fwk.NewStatus(fwk.Error, err.Error())
	}

	return nil
}

// annotatePodsWithNodes will annotate a node assignment to pods
func (sb *SlurmBridge) annotatePodsWithNodes(ctx context.Context, jobid int32, kubeNodes sets.Set[string], pods *corev1.PodList) error {
	logger := klog.FromContext(ctx)
	// Successive PreFilter cycles for siblings in the same gang may reach this
	// function. Scheduling cycles are serialized, but a sibling may have been
	// queued before it observed node assignments written by an earlier cycle,
	// while that earlier pod's binding is still in progress.
	//
	// Each call starts with the full Slurm allocation. Preserve assignments already
	// recorded on pods and remove their nodes from the available set; rebuilding
	// every assignment could otherwise reshuffle the gang and select a node already
	// chosen by an earlier scheduling cycle.
	for _, p := range pods.Items {
		podJobID := slurmjobir.ParseSlurmJobId(p.Labels[wellknown.LabelExternalJobId])
		if jobid != podJobID {
			continue
		}
		if existing := p.Annotations[wellknown.AnnotationExternalJobNode]; existing != "" && kubeNodes.Has(existing) {
			kubeNodes.Delete(existing)
		}
	}
	for _, p := range pods.Items {
		// Return if there are no nodes left
		if kubeNodes.Len() == 0 {
			logger.V(5).Info("no nodes left to annotate")
			break
		}
		// If this pod doesn't have a JobId that matches, it should be skipped as
		// it didn't exist when the external job was created
		podJobID := slurmjobir.ParseSlurmJobId(p.Labels[wellknown.LabelExternalJobId])
		if jobid != podJobID {
			logger.V(5).Info("pod JobID does not match external JobID")
			continue
		}
		if p.Annotations[wellknown.AnnotationExternalJobNode] != "" {
			continue
		}
		if p.Annotations == nil {
			p.Annotations = make(map[string]string)
		}
		node, ok := kubeNodes.PopAny()
		if !ok {
			logger.V(4).Info("could not get a node to assign")
			return ErrorNoKubeNode
		}
		toUpdate := p.DeepCopy()
		toUpdate.Annotations[wellknown.AnnotationExternalJobNode] = node
		if err := sb.Patch(ctx, toUpdate, client.StrategicMergeFrom(&p)); err != nil {
			// A sibling can vanish (e.g. recreated elsewhere) between the
			// snapshot and this patch; don't fail the whole gang for it.
			if apierrors.IsNotFound(err) {
				logger.V(4).Info("pod no longer exists, skipping node assignment",
					"pod", klog.KObj(&p))
				kubeNodes.Insert(node)
				continue
			}
			logger.Error(err, "failed to update pod with slurm job id")
			return ErrorPodUpdateFailed
		}
	}
	return nil
}

// deleteExternalJob will delete the external job associated with the pod
// and remove allocation metadata from pods sharing its job or heterogeneous job ID.
func (sb *SlurmBridge) deleteExternalJob(ctx context.Context, pod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	// Construct an intermediate representation of the Slurm external job
	slurmJobIR, err := slurmjobir.TranslateToSlurmJobIR(sb.Client, sb.registry(), sb.workloadAPI, ctx, pod)
	if err != nil {
		logger.Error(err, "failed to translate to slurmjobir")
		return err
	}
	jobId := pod.Labels[wellknown.LabelExternalJobId]
	if err := sb.slurmControl.DeleteJob(ctx, pod); err != nil {
		logger.Error(err, "failed to delete Slurm job for pod", "jobId", jobId, "pod", klog.KObj(pod))
		return err
	}
	hetJobId := pod.Labels[wellknown.LabelExternalHetJobId]
	for _, p := range slurmJobIR.AllPods() {
		toUpdate := p.DeepCopy()
		if toUpdate.Labels[wellknown.LabelExternalJobId] == "" {
			continue
		}
		if toUpdate.Labels[wellknown.LabelExternalJobId] != jobId &&
			(hetJobId == "" || toUpdate.Labels[wellknown.LabelExternalHetJobId] != hetJobId) {
			continue
		}
		delete(toUpdate.Labels, wellknown.LabelExternalJobId)
		delete(toUpdate.Labels, wellknown.LabelExternalHetJobId)
		delete(toUpdate.Labels, wellknown.LabelExternalHetJobOffset)
		delete(toUpdate.Annotations, wellknown.AnnotationExternalJobNode)
		if err := sb.Patch(ctx, toUpdate, client.StrategicMergeFrom(&p)); err != nil {
			logger.Error(err, "failed to delete jobid and node annotation")
			return err
		}
	}
	return nil
}

// PreFilterExtensions returns a PreFilterExtensions interface if the plugin implements one.
func (sb *SlurmBridge) PreFilterExtensions() fwk.PreFilterExtensions {
	return nil
}

// Filter verifies the placement decision matches the node being filtered.
// This must be the last configured Filter plugin so PostFilter can make
// the assertion that a failure from this Filter plugin implies no other
// Filter plugin removed the node from consideration before getting here.
func (sb *SlurmBridge) Filter(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, nodeInfo fwk.NodeInfo) *fwk.Status {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("filter func", "pod", klog.KObj(pod), "node", nodeInfo.Node().Name)
	assigned := pod.Annotations[wellknown.AnnotationExternalJobNode]
	if s, err := getStateData(state); err == nil {
		assigned = s.assignedNode(pod)
	}
	if assigned == nodeInfo.Node().Name {
		return fwk.NewStatus(fwk.Success, "")
	}
	return fwk.NewStatus(fwk.Unschedulable, "node does not match assignment")
}
