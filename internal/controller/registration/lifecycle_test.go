// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kubeinterceptor "sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurminterceptor "github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/registration/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type lifecycleFixture struct {
	r               *Reconciler
	kube            client.Client
	live            slurmclient.Client
	group           *slurmjobir.PodGroup
	pod             *corev1.Pod
	job             *api.V0044JobInfo
	jobs            []slurmjob.RegisteredJob
	cancellations   []int32
	cancelErr       error
	acknowledgeOnly bool
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	f := &lifecycleFixture{}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, resourcev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	workloadAPI, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
	if err != nil {
		t.Fatal(err)
	}
	f.group = &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gang", UID: "group-uid"}, Spec: slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1.PodGroupSchedulingPolicy{Gang: &schedulingv1.GangSchedulingPolicy{MinCount: 1}}}}
	f.pod = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "original", UID: "original-uid", Finalizers: []string{wellknown.FinalizerScheduler}, Labels: map[string]string{wellknown.LabelExternalJobId: "100"}}, Spec: corev1.PodSpec{SchedulerName: schedulerName, SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("gang")}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	info := externaljobinfo.ExternalJobInfo{PodGroup: externaljobinfo.NewGroupIdentity(metav1.PartialObjectMetadata{TypeMeta: f.group.TypeMeta, ObjectMeta: f.group.ObjectMeta}), Pods: []string{externaljobinfo.PodKey(f.pod)}}
	f.job = &api.V0044JobInfo{JobId: ptr.To[int32](100), AdminComment: ptr.To(info.ToString()), JobState: ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING})}
	f.kube = fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.group, f.pod).Build()
	f.live = slurmfake.NewFakeClient(f.job)
	cached := slurmfake.NewFakeClient(f.job)
	live := slurminterceptor.NewClient(f.live, slurminterceptor.Funcs{
		Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
			if (&slurmclient.GetOptions{}).ApplyOptions(opts).SkipCache {
				return f.live.Get(ctx, key, obj, opts...)
			}
			return cached.Get(ctx, key, obj, opts...)
		},
		Delete: func(ctx context.Context, obj object.Object, _ ...slurmclient.DeleteOption) error {
			id, err := strconv.ParseInt(string(obj.GetKey()), 10, 32)
			if err != nil {
				return err
			}
			f.cancellations = append(f.cancellations, int32(id))
			if f.cancelErr != nil {
				return f.cancelErr
			}
			if f.acknowledgeOnly {
				return nil
			}
			job := &api.V0044JobInfo{}
			if err := f.live.Get(ctx, obj.GetKey(), job); err != nil {
				return err
			}
			job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED})
			return f.live.Update(ctx, job, nil)
		},
	})
	informer := &registrationInformer{InformerCache: cached.GetInformer(api.ObjectTypeV0044JobInfo), synced: true}
	index, err := slurmjob.NewPodIndex(live, informer)
	if err != nil {
		t.Fatal(err)
	}
	f.r = &Reconciler{Client: f.kube, apiReader: f.kube, slurmClient: live, informer: informer, jobIndex: index, slurmControl: slurmcontrol.NewControl(live), schedulerName: schedulerName, workloadAPI: workloadAPI, recent: cache.NewExpiring(), enqueue: func(Request) {}}
	f.jobs, err = slurmjob.IndexedAllocation(t.Context(), informer, slurmjob.AllocationIdentity{Index: slurmjob.GroupJobIndex, Key: info.PodGroup.Key()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *lifecycleFixture) reconcile(t *testing.T) (bool, error) {
	t.Helper()
	pods, _, err := f.r.members(t.Context(), objectRequest(f.group))
	if err != nil && !errors.Is(err, slurmjobir.ErrorPodGroupNoPods) {
		t.Fatal(err)
	}
	return f.r.reconcileLifecycle(t.Context(), objectRequest(f.group), pods, f.jobs)
}

func (f *lifecycleFixture) latePod(t *testing.T) *corev1.Pod {
	t.Helper()
	pod := f.pod.DeepCopy()
	pod.Name = "late"
	pod.UID = "late-uid"
	pod.ResourceVersion = ""
	pod.Finalizers = nil
	pod.Labels = nil
	pod.Status.Phase = corev1.PodPending
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}
	if err := f.kube.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func TestLifecycleRetainsCurrentMembers(t *testing.T) {
	for _, scenario := range []string{"late-member", "late-member-only-in-api", "replaced-pod-in-same-group", "deleting-bound-member"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLifecycleFixture(t)
			switch scenario {
			case "late-member":
				f.latePod(t)
			case "late-member-only-in-api":
				stale := fake.NewClientBuilder().WithScheme(f.kube.Scheme()).WithObjects(f.group, f.pod).Build()
				f.latePod(t)
				f.r.Client = stale
			case "replaced-pod-in-same-group":
				if err := f.kube.Delete(t.Context(), f.pod); err != nil {
					t.Fatal(err)
				}
				old := &corev1.Pod{}
				if err := f.kube.Get(t.Context(), client.ObjectKeyFromObject(f.pod), old); err != nil {
					t.Fatal(err)
				}
				old.Finalizers = nil
				if err := f.kube.Update(t.Context(), old); err != nil {
					t.Fatal(err)
				}
				late := f.latePod(t)
				if err := f.kube.Delete(t.Context(), late); err != nil {
					t.Fatal(err)
				}
				late.Name = f.pod.Name
				late.UID = "replacement-uid"
				late.ResourceVersion = ""
				if err := f.kube.Create(t.Context(), late); err != nil {
					t.Fatal(err)
				}
			case "deleting-bound-member":
				pod := f.latePod(t)
				pod.Spec.NodeName = "node"
				pod.Finalizers = []string{wellknown.FinalizerScheduler}
				if err := f.kube.Update(t.Context(), pod); err != nil {
					t.Fatal(err)
				}
				if err := f.kube.Delete(t.Context(), pod); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.reconcile(t); err != nil {
				t.Fatal(err)
			}
			if len(f.cancellations) != 0 {
				t.Fatalf("canceled allocation with a current member: %v", f.cancellations)
			}
		})
	}
}

func TestLifecycleReleaseBeforeFinalization(t *testing.T) {
	for _, scenario := range []string{"failure", "acknowledged", "cache-miss", "finished-cache-live-running", "success", "deleting-unbound"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLifecycleFixture(t)
			claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "claim", UID: "claim-uid", Labels: map[string]string{wellknown.LabelAllocationPodUID: string(f.pod.UID)}, OwnerReferences: []metav1.OwnerReference{{Kind: "Pod", UID: f.pod.UID}}}}
			if err := f.kube.Create(t.Context(), claim); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "failure":
				f.cancelErr = errors.New("Slurm unavailable")
			case "acknowledged":
				f.acknowledgeOnly = true
			case "cache-miss":
				f.jobs = nil
			case "finished-cache-live-running":
				f.jobs[0].Finished = true
				f.acknowledgeOnly = true
			case "deleting-unbound":
				current := &corev1.Pod{}
				if err := f.kube.Get(t.Context(), client.ObjectKeyFromObject(f.pod), current); err != nil {
					t.Fatal(err)
				}
				current.Status.Phase = corev1.PodPending
				if err := f.kube.Status().Update(t.Context(), current); err != nil {
					t.Fatal(err)
				}
				if err := f.kube.Delete(t.Context(), current); err != nil {
					t.Fatal(err)
				}
			}
			waiting, err := f.reconcile(t)
			if (err != nil) != (scenario == "failure") {
				t.Fatalf("unexpected error: %v", err)
			}
			released := scenario == "success" || scenario == "deleting-unbound"
			if scenario != "failure" && waiting == released {
				t.Fatalf("waiting=%t, released=%t", waiting, released)
			}
			err = f.kube.Get(t.Context(), client.ObjectKeyFromObject(claim), &resourcev1.ResourceClaim{})
			if apierrors.IsNotFound(err) != released {
				t.Fatalf("claim release=%t, error=%v", released, err)
			}
			current := &corev1.Pod{}
			err = f.kube.Get(t.Context(), client.ObjectKeyFromObject(f.pod), current)
			if err == nil && slices.Contains(current.Finalizers, wellknown.FinalizerScheduler) == released {
				t.Fatalf("incorrect finalizers: %v", current.Finalizers)
			}
			// A new controller has no submission memo; the same indexed ownership must
			// allow recovery after a lost cancellation response or transient failure.
			f.r.recent = cache.NewExpiring()
			f.cancelErr = nil
			f.acknowledgeOnly = false
			if scenario == "cache-miss" {
				return
			} // Recovery requires the next informer update.
			if _, err := f.reconcile(t); err != nil {
				t.Fatal(err)
			}
			if err := f.kube.Get(t.Context(), client.ObjectKeyFromObject(claim), &resourcev1.ResourceClaim{}); !apierrors.IsNotFound(err) {
				t.Fatalf("claim not released on recovery: %v", err)
			}
		})
	}
}

func TestLifecycleRejectsChangedSlurmOwnership(t *testing.T) {
	f := newLifecycleFixture(t)
	job := f.job.DeepCopy()
	job.AdminComment = ptr.To((&externaljobinfo.ExternalJobInfo{Pods: []string{"ns/someone-else/uid"}}).ToString())
	if err := f.live.Update(t.Context(), job, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(t); err == nil {
		t.Fatal("expected changed ownership error")
	}
	if len(f.cancellations) != 0 {
		t.Fatal("canceled another owner's job")
	}
}

func TestLifecycleRoutesDeletedMemberToGroup(t *testing.T) {
	f := newLifecycleFixture(t)
	var queued []Request
	f.r.enqueue = func(key Request) { queued = append(queued, key) }
	key := podRequest(f.pod)
	key.UID = "missing-uid"
	// The exact old identity still indexes the allocation even when its Pod is gone.
	if err := f.kube.Delete(t.Context(), f.pod); err != nil {
		t.Fatal(err)
	}
	current := &corev1.Pod{}
	if err := f.kube.Get(t.Context(), client.ObjectKeyFromObject(f.pod), current); err != nil {
		t.Fatal(err)
	}
	current.Finalizers = nil
	if err := f.kube.Update(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Reconcile(t.Context(), podRequest(f.pod)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(queued, []Request{objectRequest(f.group)}) || len(f.cancellations) != 0 {
		t.Fatalf("queued=%v, canceled=%v", queued, f.cancellations)
	}
}

func TestLifecycleRetainsClaimsOnConcurrentReplacement(t *testing.T) {
	f := newLifecycleFixture(t)
	// A changed Pod must not be finalized using a previous membership snapshot.
	f.r.apiReader = kubeinterceptor.NewClient(f.kube.(client.WithWatch), kubeinterceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if pod, ok := obj.(*corev1.Pod); ok {
			pod.ResourceVersion = "newer"
		}
		return nil
	}})
	if _, err := f.reconcile(t); err == nil {
		t.Fatal("expected optimistic finalization conflict")
	}
	pod := &corev1.Pod{}
	if err := f.kube.Get(t.Context(), client.ObjectKeyFromObject(f.pod), pod); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pod.Finalizers, wellknown.FinalizerScheduler) {
		t.Fatal("lost finalizer")
	}
}

type componentReleaseRecorder struct {
	slurmcontrol.SlurmControlInterface
	fixture *lifecycleFixture
	offsets []int32
}

func (c *componentReleaseRecorder) TerminateHetJobComponent(ctx context.Context, leader, offset int32) error {
	c.offsets = append(c.offsets, offset)
	job := c.fixture.job.DeepCopy()
	job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED})
	return c.fixture.live.Update(ctx, job, nil)
}

func TestLifecycleHeterogeneousRelease(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "running-component-zero", true: "pending-incomplete-cache"}[pending], func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.job.HetJobId = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To[int32](100)}
			f.job.HetJobOffset = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To[int32](0)}
			if pending {
				f.job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING})
			}
			if err := f.live.Update(t.Context(), f.job, nil); err != nil {
				t.Fatal(err)
			}
			first, err := slurmjob.RegisteredJobFromInfo(f.job)
			if err != nil {
				t.Fatal(err)
			}
			// The second component is absent from the informer but present in Slurm.
			siblingPod := f.latePod(t)
			siblingPod.Spec.SchedulingGroup = nil
			if err := f.kube.Update(t.Context(), siblingPod); err != nil {
				t.Fatal(err)
			}
			sibling := f.job.DeepCopy()
			sibling.JobId = ptr.To[int32](101)
			sibling.HetJobOffset.Number = ptr.To[int32](1)
			sibling.AdminComment = ptr.To((&externaljobinfo.ExternalJobInfo{Pods: []string{externaljobinfo.PodKey(siblingPod)}}).ToString())
			if err := f.live.Create(t.Context(), sibling, nil); err != nil {
				t.Fatal(err)
			}
			f.jobs = []slurmjob.RegisteredJob{first}
			control := &componentReleaseRecorder{SlurmControlInterface: f.r.slurmControl, fixture: f}
			f.r.slurmControl = control
			if _, err := f.reconcile(t); err != nil {
				t.Fatal(err)
			}
			if len(f.cancellations) != 0 {
				t.Fatal("canceled whole heterogeneous allocation")
			}
			if pending && len(control.offsets) != 0 || !pending && !slices.Equal(control.offsets, []int32{0}) {
				t.Fatalf("component offsets=%v", control.offsets)
			}
			current := &api.V0044JobInfo{}
			if err := f.live.Get(t.Context(), sibling.GetKey(), current); err != nil {
				t.Fatal(err)
			}
			job, err := slurmjob.FromJobInfo(current)
			if err != nil {
				t.Fatal(err)
			}
			if job.Finished {
				t.Fatal("released live sibling")
			}
		})
	}
}

func TestLifecyclePendingCompositeLeaves(t *testing.T) {
	for _, represented := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-leaf", true: "existing-leaf"}[represented], func(t *testing.T) {
			f := newLifecycleFixture(t)
			root := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "scheduling.k8s.io/v1alpha3", "kind": "CompositePodGroup",
				"metadata": map[string]any{"namespace": "ns", "name": "root", "uid": "root-uid"},
				"spec":     map[string]any{"schedulingPolicy": map[string]any{"gang": map[string]any{"minGroupCount": int64(1)}}},
			}}
			if err := f.kube.Create(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			f.group.Spec.ParentCompositePodGroupName = ptr.To(root.GetName())
			if err := f.kube.Update(t.Context(), f.group); err != nil {
				t.Fatal(err)
			}
			leaf := f.group.DeepCopy()
			leaf.Name, leaf.UID, leaf.ResourceVersion = "late-leaf", "late-leaf-uid", ""
			if err := f.kube.Create(t.Context(), leaf); err != nil {
				t.Fatal(err)
			}
			late := f.latePod(t)
			late.Spec.SchedulingGroup.PodGroupName = ptr.To(leaf.Name)
			if err := f.kube.Update(t.Context(), late); err != nil {
				t.Fatal(err)
			}

			info := f.jobs[0].Ownership()
			info.CompositePodGroup = &externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "CompositePodGroup", Namespace: "ns", Name: "root", UID: "root-uid"}
			f.job.AdminComment = ptr.To(info.ToString())
			f.job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING})
			f.job.HetJobId = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To[int32](100)}
			f.job.HetJobOffset = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To[int32](0)}
			if err := f.live.Update(t.Context(), f.job, nil); err != nil {
				t.Fatal(err)
			}
			first, err := slurmjob.RegisteredJobFromInfo(f.job)
			if err != nil {
				t.Fatal(err)
			}
			// The second component is absent from the informer. Its recorded Pod is
			// gone, but a late member of its leaf must still prevent cancellation.
			sibling := f.job.DeepCopy()
			sibling.JobId = ptr.To[int32](101)
			sibling.HetJobOffset.Number = ptr.To[int32](1)
			info.PodGroup = &externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "PodGroup", Namespace: "ns", Name: "gone", UID: "gone-uid"}
			if represented {
				info.PodGroup.Name, info.PodGroup.UID = leaf.Name, leaf.UID
			}
			info.Pods = []string{"ns/gone-pod/gone-pod-uid"}
			sibling.AdminComment = ptr.To(info.ToString())
			if err := f.live.Create(t.Context(), sibling, nil); err != nil {
				t.Fatal(err)
			}

			released, err := f.r.releaseComponent(t.Context(), first)
			if err != nil {
				t.Fatal(err)
			}
			var wantCancellations []int32
			if !represented {
				wantCancellations = []int32{100}
			}
			if released == represented || !slices.Equal(f.cancellations, wantCancellations) {
				t.Fatalf("released=%t, cancellations=%v; leaf represented=%t", released, f.cancellations, represented)
			}
		})
	}
}

func TestLifecycleIndexedMembership(t *testing.T) {
	f := newLifecycleFixture(t)
	late := f.latePod(t)
	indexed := fake.NewClientBuilder().WithScheme(f.kube.Scheme()).WithObjects(f.group, f.pod, late).
		WithIndex(&corev1.Pod{}, podGroupMemberIndex, nativePodGroupKeys).WithIndex(&corev1.Pod{}, publishedJobIndex, publishedJobKeys).Build()
	lists := 0
	f.r.Client = kubeinterceptor.NewClient(indexed, kubeinterceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*corev1.PodList); ok {
			lists++
			options := (&client.ListOptions{}).ApplyOptions(opts)
			if options.FieldSelector == nil {
				t.Fatal("cached membership performed an unindexed Pod list")
			}
		}
		return c.List(ctx, list, opts...)
	}})
	f.r.membersIndexed = true
	if _, err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if lists == 0 || len(f.cancellations) != 0 {
		t.Fatalf("lists=%d, cancellations=%v", lists, f.cancellations)
	}
}

func TestLifecycleWaitsForSlurmCompleting(t *testing.T) {
	f := newLifecycleFixture(t)
	job := f.job.DeepCopy()
	job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED, api.V0044JobInfoJobStateCOMPLETING})
	if err := f.live.Update(t.Context(), job, nil); err != nil {
		t.Fatal(err)
	}
	f.jobs[0].Finished = true
	waiting, err := f.reconcile(t)
	if err != nil || !waiting {
		t.Fatalf("waiting=%t error=%v", waiting, err)
	}
	pod := &corev1.Pod{}
	if err := f.kube.Get(t.Context(), client.ObjectKeyFromObject(f.pod), pod); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pod.Finalizers, wellknown.FinalizerScheduler) {
		t.Fatal("released finalizer during Slurm epilog")
	}
	job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED})
	if err := f.live.Update(t.Context(), job, nil); err != nil {
		t.Fatal(err)
	}
	if waiting, err := f.reconcile(t); err != nil || waiting {
		t.Fatalf("waiting=%t error=%v", waiting, err)
	}
}
