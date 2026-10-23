// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/testutils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestRegistrationAPI(t *testing.T) {
	testEnv := &envtest.Environment{BinaryAssetsDirectory: testutils.GetEnvTestBinary(filepath.Join("..", "..", ".."))}
	testEnv.ControlPlane.GetAPIServer().Configure().
		Set("feature-gates", "GenericWorkload=true").
		Set("runtime-config", "scheduling.k8s.io/v1beta1=true")
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Error(err)
		}
	})
	cfg.ContentType, cfg.AcceptContentTypes = runtime.ContentTypeJSON, runtime.ContentTypeJSON
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workloadAPI, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.GetCache().Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("cache did not stop")
		}
	})
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	create := func(obj client.Object) {
		t.Helper()
		if err := writer.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		observed := obj.DeepCopyObject().(client.Object)
		if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
			err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(obj), observed)
			return err == nil && observed.GetUID() == obj.GetUID(), client.IgnoreNotFound(err)
		}); err != nil {
			t.Fatal(err)
		}
	}
	create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "registration"}})
	// Routing must not access Slurm at all. Leave both its client and informer nil.
	queued := make(map[Request]bool)
	r := &Reconciler{Client: mgr.GetClient(), schedulerName: "slurm-bridge", workloadAPI: workloadAPI,
		recent: cache.NewExpiring(), enqueue: func(key Request) { queued[key] = true }}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "registration", Name: "first"}, Spec: corev1.PodSpec{
		SchedulerName: r.schedulerName, Containers: []corev1.Container{{Name: "worker", Image: "busybox"}},
		SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("gang")},
	}}
	create(pod)
	if result, err := r.Reconcile(ctx, podRequest(pod)); err != nil || result.RequeueAfter != registrationDependencyRetry || len(queued) != 0 {
		t.Fatalf("missing group: result=%v, error=%v, queued=%v", result, err, queued)
	}
	group := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta,
		ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: "gang", Annotations: map[string]string{wellknown.AnnotationPriority: "invalid"}},
		Spec: slurmjobir.PodGroupSpec{WorkloadRef: &slurmjobir.WorkloadReference{WorkloadName: "workload", TemplateName: "gang"},
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}}},
	}
	create(group)
	want := Request{APIGroup: "scheduling.k8s.io", Kind: "PodGroup", Namespace: group.Namespace, Name: group.Name, UID: group.UID}
	bound := pod.DeepCopy()
	bound.Name, bound.UID, bound.ResourceVersion = "bound", "", ""
	bound.Spec.NodeName = "node"
	create(bound)
	// Both members reach the same allocation key, including a bound member and
	// a group whose submission annotations cannot be translated yet.
	for _, member := range []*corev1.Pod{pod, bound} {
		clear(queued)
		if result, err := r.Reconcile(ctx, podRequest(member)); err != nil || result.RequeueAfter != 0 {
			t.Fatalf("route %s: result=%v, error=%v", member.Name, result, err)
		}
		if len(queued) != 1 || !queued[want] {
			t.Fatalf("route %s: queued %v, want only %v", member.Name, queued, want)
		}
	}

	// Stale and deleted Pod requests keep their own identity for orphan cleanup.
	stale := podRequest(bound)
	stale.UID = "retired"
	if err := writer.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		err := r.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []Request{stale, podRequest(pod)} {
		members, identity, err := r.members(ctx, key)
		if err != nil || len(members) != 0 || identity.Index != slurmjob.PodJobIndex || identity.Key != key.podKey() {
			t.Fatalf("orphan %v: members=%v, identity=%v, error=%v", key, members, identity, err)
		}
	}

	standalone := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: "standalone"}, Spec: corev1.PodSpec{
		SchedulerName: r.schedulerName, Containers: []corev1.Container{{Name: "worker", Image: "busybox"}},
		SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}, {Name: "other/gate"}},
	}}
	create(standalone)
	key := podRequest(standalone)
	members, identity, err := r.members(ctx, key)
	if err != nil || len(members) != 1 || identity.Index != slurmjob.PodJobIndex || identity.Key != key.podKey() || len(queued) != 1 {
		t.Fatalf("standalone: members=%v, identity=%v, queued=%v, error=%v", members, identity, queued, err)
	}
	// Exercise registration's actual optimistic Kubernetes patch, using a
	// supplied allocation record rather than a simulated Slurm service.
	ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: members}}}}
	jobs := []slurmjob.RegisteredJob{{ExternalJob: slurmjob.ExternalJob{JobId: 100}}}
	if _, err := r.register(ctx, key, standalone, ir, jobs); err != nil {
		t.Fatal(err)
	}
	if err := writer.Get(ctx, client.ObjectKeyFromObject(standalone), standalone); err != nil {
		t.Fatal(err)
	}
	if standalone.Labels[wellknown.LabelExternalJobId] != "100" || len(standalone.Spec.SchedulingGates) != 1 || standalone.Spec.SchedulingGates[0].Name != "other/gate" {
		t.Fatalf("registration metadata=%v, gates=%v", standalone.Labels, standalone.Spec.SchedulingGates)
	}
}
