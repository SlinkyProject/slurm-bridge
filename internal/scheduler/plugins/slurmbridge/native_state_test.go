// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type nativeStateControl struct {
	postFilterSlurmControl
	job             slurmcontrol.ExternalJob
	allocationReads atomic.Int32
	liveReads       atomic.Int32
	resourceReads   atomic.Int32
	resourceErr     error
	liveErr         error
	liveJob         *slurmcontrol.ExternalJob
}

func (c *nativeStateControl) GetAllocation(context.Context, *corev1.Pod, *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	c.allocationReads.Add(1)
	return []slurmcontrol.ExternalJob{c.job}, c.allocationErr
}

func (c *nativeStateControl) GetJob(context.Context, *corev1.Pod) (*slurmcontrol.ExternalJob, error) {
	c.liveReads.Add(1)
	if c.liveJob != nil {
		return c.liveJob, c.liveErr
	}
	return ptr.To(c.job), c.liveErr
}

func (c *nativeStateControl) GetResourcesForNodes(ctx context.Context, pod *corev1.Pod, nodes []string) (map[string]*slurmcontrol.NodeResources, error) {
	c.resourceReads.Add(1)
	if c.resourceErr != nil {
		return nil, c.resourceErr
	}
	return c.postFilterSlurmControl.GetResourcesForNodes(ctx, pod, nodes)
}

func nativeFixture(t *testing.T, version string) (*SlurmBridge, *nativeStateControl, []*corev1.Pod, *atomic.Int32) {
	t.Helper()
	scheme, err := newClientScheme()
	if err != nil {
		t.Fatal(err)
	}
	api := mustRegisterTestWorkloadAPI(t, scheme, version)
	pg := &slurmjobir.PodGroup{
		TypeMeta:   api.PodGroupTypeMeta,
		ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "gang", UID: "gang-uid"},
		Spec:       slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}}},
	}
	pods := []*corev1.Pod{allocationPod("a", 0, "10", ""), allocationPod("b", 0, "10", "")}
	objects := []client.Object{pg}
	for _, pod := range pods {
		pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To("gang")}
		objects = append(objects, pod.DeepCopy())
	}
	nodes := []*corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}, {ObjectMeta: metav1.ObjectMeta{Name: "node2"}}}
	for _, node := range nodes {
		objects = append(objects, node)
	}
	lists := &atomic.Int32{}
	kc := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(pg).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok {
				lists.Add(1)
			}
			return c.List(ctx, list, opts...)
		},
	}).Build()
	handle, err := fwkruntime.NewFramework(t.Context(), nil, nil,
		fwkruntime.WithClientSet(clientsetfake.NewClientset()),
		fwkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(nil, nodes)),
		fwkruntime.WithPodActivator(&activateRecorder{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	control := &nativeStateControl{job: slurmcontrol.ExternalJob{JobId: 10, Nodes: "node[1-2]"}}
	return &SlurmBridge{Client: kc, workloadAPI: api, slurmControl: control, handle: handle, kubeNodeIndex: testKubeNodeIndex(t, kc)}, control, pods, lists
}

func nativePodState(group fwk.PodGroupCycleState) *framework.CycleState {
	state := framework.NewCycleState()
	state.SetPodGroupSchedulingCycle(group)
	return state
}

func TestNativeGangSharesPreparationAndBindingResources(t *testing.T) {
	for _, version := range []string{slurmjobir.WorkloadAPIVersionV1Alpha2, slurmjobir.WorkloadAPIVersionV1Beta1} {
		t.Run(version, func(t *testing.T) {
			sb, control, pods, lists := nativeFixture(t, version)
			before := []*corev1.Pod{pods[0].DeepCopy(), pods[1].DeepCopy()}
			group := framework.NewCycleState()
			states := []*framework.CycleState{nativePodState(group), nativePodState(group)}
			done := make(chan struct{})
			var readers sync.WaitGroup
			readers.Go(func() {
				for {
					select {
					case <-done:
						return
					default:
						for _, pod := range pods {
							_ = allocatedJobID(pod)
							_ = pod.Annotations[wellknown.AnnotationExternalJobNode]
						}
					}
				}
			})
			defer func() { close(done); readers.Wait() }()
			for i, pod := range pods {
				result, status := sb.PreFilter(t.Context(), states[i], pod, nil)
				node := "node1"
				if i == 1 {
					node = "node2"
				}
				if !status.IsSuccess() || result == nil || !result.NodeNames.Equal(sets.New(node)) {
					t.Fatalf("PreFilter %s: %v, %v", pod.Name, result, status)
				}
				info := framework.NewNodeInfo()
				info.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node}})
				if status := sb.Filter(t.Context(), states[i], pod, info); !status.IsSuccess() {
					t.Fatalf("Filter: %v", status)
				}
				stored := &corev1.Pod{}
				if err := sb.Get(t.Context(), client.ObjectKeyFromObject(pod), stored); err != nil {
					t.Fatal(err)
				}
				if stored.Annotations[wellknown.AnnotationExternalJobNode] != "" {
					t.Fatalf("native placement published as annotation: %v", stored.Annotations)
				}
			}
			if control.allocationReads.Load() != 1 || lists.Load() != 1 {
				t.Fatalf("allocation reads=%d, membership lists=%d; want 1 each", control.allocationReads.Load(), lists.Load())
			}
			if !reflect.DeepEqual(pods, before) {
				t.Fatal("framework Pods mutated")
			}
			first, _ := getStateData(states[0])
			second, _ := getStateData(states[1])
			if first.native != second.native || first.slurmJobIR != second.slurmJobIR {
				t.Fatal("members do not share immutable snapshot")
			}
			snapshot := first.slurmJobIR.Components[0].Pods.DeepCopy()
			// The framework detaches group state before concurrent binding. Retained
			// member state must still share one component resource read.
			var binding sync.WaitGroup
			for i, pod := range pods {
				states[i].SetPodGroupSchedulingCycle(nil)
				binding.Go(func() {
					node := "node1"
					if i == 1 {
						node = "node2"
					}
					if status := sb.PreBind(t.Context(), states[i], pod, node); !status.IsSuccess() {
						t.Errorf("PreBind: %v", status)
					}
				})
			}
			binding.Wait()
			if control.resourceReads.Load() != 1 || control.liveReads.Load() != 1 || control.allocationReads.Load() != 2 {
				t.Fatalf("resource reads=%d, live validation reads=%d; want 1, 1", control.resourceReads.Load(), control.liveReads.Load())
			}
			if !reflect.DeepEqual(snapshot, &first.slurmJobIR.Components[0].Pods) {
				t.Fatal("binding mutated shared membership")
			}
		})
	}
}

func TestNativeGangRetriesRebuildSnapshot(t *testing.T) {
	sb, control, pods, lists := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
	control.job.Pending, control.job.Nodes = true, ""
	group := framework.NewCycleState()
	state := nativePodState(group)
	if result, status := sb.PreFilter(t.Context(), state, pods[0], nil); !status.IsSuccess() || result != nil {
		t.Fatalf("pending: %v %v", result, status)
	}
	old, _ := getStateData(state)
	// A change to Slurm observation cannot switch the current gang's decision.
	control.job.Pending, control.job.Nodes = false, "node[1-2]"
	if result, status := sb.PreFilter(t.Context(), nativePodState(group), pods[1], nil); !status.IsSuccess() || result != nil {
		t.Fatalf("mixed allocation snapshots: %v %v", result, status)
	}
	next := nativePodState(framework.NewCycleState())
	if result, status := sb.PreFilter(t.Context(), next, pods[0], nil); !status.IsSuccess() || result == nil {
		t.Fatalf("retry: %v %v", result, status)
	}
	refreshed, _ := getStateData(next)
	if refreshed.native == old.native || control.allocationReads.Load() != 2 || lists.Load() != 2 {
		t.Fatal("retry reused stale gang state")
	}
}

func TestNativeGangPlacementNeedsNoMetadataWrites(t *testing.T) {
	for _, version := range []string{slurmjobir.WorkloadAPIVersionV1Alpha2, slurmjobir.WorkloadAPIVersionV1Beta1} {
		t.Run(version, func(t *testing.T) {
			sb, _, pods, _ := nativeFixture(t, version)
			for _, pod := range pods {
				stored := pod.DeepCopy()
				if err := sb.Get(t.Context(), client.ObjectKeyFromObject(pod), stored); err != nil {
					t.Fatal(err)
				}
				// Simulate old assignments from an earlier scheduler version. Both
				// members deliberately carry the same invalid node annotation.
				stored.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "obsolete-node"}
				if err := sb.Update(t.Context(), stored); err != nil {
					t.Fatal(err)
				}
				pod.Annotations = stored.Annotations
			}
			sb.Client = interceptor.NewClient(sb.Client.(client.WithWatch), interceptor.Funcs{
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					t.Error("native placement attempted a metadata patch")
					return errors.New("Pod resource version changed")
				},
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					t.Error("native placement attempted a status patch")
					return errors.New("PodGroup status belongs to the framework")
				},
			})
			group := framework.NewCycleState()
			for i, pod := range pods {
				state := nativePodState(group)
				result, status := sb.PreFilter(t.Context(), state, pod, nil)
				if !status.IsSuccess() || result == nil || !result.NodeNames.Equal(sets.New(fmt.Sprintf("node%d", i+1))) {
					t.Fatalf("PreFilter: %v %v", result, status)
				}
				// Change metadata after snapshotting the gang, before the next
				// member enters PreFilter. This must not invalidate placement.
				stored := &corev1.Pod{}
				if err := sb.Get(t.Context(), client.ObjectKeyFromObject(pods[1]), stored); err != nil {
					t.Fatal(err)
				}
				stored.Annotations["test-update"] = fmt.Sprint(i)
				if err := sb.Update(t.Context(), stored); err != nil {
					t.Fatal(err)
				}
				if status := sb.PreBind(t.Context(), state, pod, fmt.Sprintf("node%d", i+1)); !status.IsSuccess() {
					t.Fatalf("PreBind: %v", status)
				}
			}
		})
	}
}

func TestNativePostFilterUsesCyclePlacement(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%t", pending), func(t *testing.T) {
			sb, control, pods, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
			pod := pods[0]
			pod.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node2"}
			if pending {
				control.job.Pending, control.job.Nodes = true, ""
			}
			state := nativePodState(framework.NewCycleState())
			if _, status := sb.PreFilter(t.Context(), state, pod, nil); !status.IsSuccess() {
				t.Fatal(status)
			}
			// An allocation that starts after PreFilter must survive this cycle.
			control.job.Pending, control.job.Nodes = false, "node[1-2]"
			m := framework.NewNodeToStatus(map[string]*fwk.Status{
				"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin("OtherPlugin"),
				"node2": fwk.NewStatus(fwk.Success),
			}, fwk.NewStatus(fwk.Unschedulable))
			_, _ = sb.PostFilter(t.Context(), state, pod, m)
			if deleted := control.deletedPod != nil; deleted == pending {
				t.Fatalf("allocation deleted=%t with pending=%t", deleted, pending)
			}
			if pod.Annotations[wellknown.AnnotationExternalJobNode] != "node2" {
				t.Fatal("PostFilter mutated input Pod")
			}
		})
	}
}

func TestNativeGangRejectsChangedMember(t *testing.T) {
	for _, change := range []string{"uid", "job", "spec", "late member"} {
		t.Run(change, func(t *testing.T) {
			sb, control, pods, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
			control.job.Pending, control.job.Nodes = true, ""
			control.updateJob = func(*slurmjobir.SlurmJobIR) (int32, error) {
				t.Error("released gang with changed member")
				return 10, nil
			}
			group := framework.NewCycleState()
			if _, status := sb.PreFilter(t.Context(), nativePodState(group), pods[0], nil); !status.IsSuccess() {
				t.Fatal(status)
			}
			changed := pods[1].DeepCopy()
			switch change {
			case "uid":
				changed.UID = types.UID("replacement")
			case "job":
				changed.Labels[wellknown.LabelExternalJobId] = "11"
			case "spec":
				changed.Spec.NodeSelector = map[string]string{"changed": "yes"}
			case "late member":
				changed.Name, changed.UID = "late", "late"
				if err := sb.Create(t.Context(), changed); err != nil {
					t.Fatal(err)
				}
			}
			state := nativePodState(group)
			if _, status := sb.PreFilter(t.Context(), state, changed, nil); status.Code() != fwk.Unschedulable {
				t.Fatalf("changed member accepted: %v", status)
			}
			s, _ := getStateData(state)
			if s.slurmJobIR != nil {
				t.Fatal("rejected member can reach hold release")
			}
			info := &framework.PodGroupInfo{Namespace: "test", Name: "gang", Type: fwk.PodGroupKeyType,
				PodGroup: &schedulingv1beta1.PodGroup{}, UnscheduledPods: []*corev1.Pod{pods[0], changed}}
			if _, status := sb.PodGroupPostFilter(t.Context(), group, info, nil); status.Code() != fwk.Unschedulable {
				t.Fatalf("changed member did not block its allocation: %v", status)
			}

		})
	}
}

func TestNativeGangResourceFailureDoesNotPoisonRetry(t *testing.T) {
	sb, control, pods, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
	group := framework.NewCycleState()
	states := []*framework.CycleState{nativePodState(group), nativePodState(group)}
	for i, pod := range pods {
		if _, status := sb.PreFilter(t.Context(), states[i], pod, nil); !status.IsSuccess() {
			t.Fatal(status)
		}
	}
	control.resourceErr = errors.New("resource lookup unavailable")
	for i, pod := range pods {
		node := "node1"
		if i == 1 {
			node = "node2"
		}
		if status := sb.PreBind(t.Context(), states[i], pod, node); status.Code() != fwk.Error {
			t.Fatalf("missing resources accepted: %v", status)
		}
	}
	if control.resourceReads.Load() != 1 {
		t.Fatal("failed resource fetch repeated within attempt")
	}
	control.resourceErr = nil
	next := nativePodState(framework.NewCycleState())
	if _, status := sb.PreFilter(t.Context(), next, pods[0], nil); !status.IsSuccess() {
		t.Fatal(status)
	}
	if status := sb.PreBind(t.Context(), next, pods[0], "node1"); !status.IsSuccess() {
		t.Fatal(status)
	}
	if control.resourceReads.Load() != 2 {
		t.Fatal("new attempt did not retry resource lookup")
	}
}

func TestNativeAssignmentsReserveBoundNodes(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node1"}}, Spec: corev1.PodSpec{NodeName: "node2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
	}
	snapshot := internalcache.NewSnapshot(nil, []*corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
	})
	assigned, err := nativeAssignments(pods, sets.New("node1", "node2"), snapshot.NodeInfos())
	if err != nil || assigned[client.ObjectKey{Name: "a"}] != "node1" || assigned[client.ObjectKey{Name: "b"}] != "node2" {
		t.Fatalf("assignments=%v err=%v", assigned, err)
	}
	pods[1].Spec.NodeName = "node2"
	if _, err := nativeAssignments(pods, sets.New("node1", "node2"), snapshot.NodeInfos()); err == nil {
		t.Fatal("duplicate assignment accepted")
	}
	pods[1].Spec.NodeName = ""
	if _, err := nativeAssignments(pods, sets.New("node2"), snapshot.NodeInfos()); err == nil {
		t.Fatal("insufficient capacity accepted")
	}
}

func TestNativeAssignmentsRecoverAssumedPods(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", UID: "b"}},
	}
	assumed := pods[1].DeepCopy()
	assumed.Spec.NodeName = "node1"
	nodes := []*corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}, {ObjectMeta: metav1.ObjectMeta{Name: "node2"}}}
	snapshot := internalcache.NewSnapshot([]*corev1.Pod{assumed}, nodes)
	for _, ordered := range [][]corev1.Pod{pods, {pods[1], pods[0]}} {
		assigned, err := nativeAssignments(ordered, sets.New("node1", "node2"), snapshot.NodeInfos())
		if err != nil || assigned[client.ObjectKey{Name: "a"}] != "node2" || assigned[client.ObjectKey{Name: "b"}] != "node1" {
			t.Fatalf("assumed placement lost: %v %v", assigned, err)
		}
	}
	// A replacement UID must not inherit the deleted member's placement.
	pods[1].UID = "replacement"
	assigned, err := nativeAssignments(pods, sets.New("node1", "node2"), snapshot.NodeInfos())
	if err != nil || assigned[client.ObjectKey{Name: "b"}] != "node2" {
		t.Fatalf("replacement inherited old placement: %v %v", assigned, err)
	}
}

type nativeManagerHandle struct {
	framework.Framework
	manager fwk.PodGroupManager
	bridge  *SlurmBridge
}

func (h nativeManagerHandle) PodGroupManager() fwk.PodGroupManager { return h.manager }
func (h nativeManagerHandle) RunFilterPluginsWithNominatedPods(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, node fwk.NodeInfo) *fwk.Status {
	return h.RunFilterPlugins(ctx, state, pod, node)
}
func (h nativeManagerHandle) RunPreFilterPlugins(ctx context.Context, state fwk.CycleState, pod *corev1.Pod) (*fwk.PreFilterResult, *fwk.Status, sets.Set[string]) {
	result, status := h.bridge.PreFilter(ctx, state, pod, nil)
	return result, status, nil
}

type nativeHetControl struct {
	postFilterSlurmControl
	reads   int
	effects []string
}

func (c *nativeHetControl) GetAllocation(_ context.Context, _ *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	c.reads++
	jobs := testAllocation(ir)
	for i := range jobs {
		jobs[i].Pending, jobs[i].Held, jobs[i].HetJobId = true, true, 10
	}
	return jobs, nil
}
func (c *nativeHetControl) UpdateJob(_ context.Context, pod *corev1.Pod, _ *slurmjobir.SlurmJobIR) (int32, error) {
	c.effects = append(c.effects, "update:"+pod.Labels[wellknown.LabelExternalJobId])
	return slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId]), nil
}
func (c *nativeHetControl) ReleaseJob(_ context.Context, id int32, _ *int32) error {
	c.effects = append(c.effects, fmt.Sprintf("release:%d", id))
	return nil
}

func TestNativeCompositeSharesSnapshotAndReleasesAfterAllComponents(t *testing.T) {
	metrics.Register()
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.GenericWorkload, true)
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.TopologyAwareWorkloadScheduling, true)
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.CompositePodGroup, true)
	for _, unevaluated := range []bool{false, true} {
		t.Run(fmt.Sprintf("unevaluated=%t", unevaluated), func(t *testing.T) {
			sb, _, pods, lists := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
			if err := schedulingv1alpha3.AddToScheme(sb.Scheme()); err != nil {
				t.Fatal(err)
			}
			root := &schedulingv1alpha3.CompositePodGroup{
				TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha3", Kind: "CompositePodGroup"},
				ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "root", UID: "root"},
				Spec:       schedulingv1alpha3.CompositePodGroupSpec{SchedulingPolicy: schedulingv1alpha3.CompositePodGroupSchedulingPolicy{Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 2}}},
			}
			if err := sb.Create(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			first := &slurmjobir.PodGroup{TypeMeta: sb.workloadAPI.PodGroupTypeMeta}
			if err := sb.Get(t.Context(), client.ObjectKey{Namespace: "test", Name: "gang"}, first); err != nil {
				t.Fatal(err)
			}
			first.Spec.ParentCompositePodGroupName = ptr.To("root")
			first.Spec.SchedulingPolicy.Gang.MinCount = 1
			if err := sb.Update(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			second := first.DeepCopyObject().(*slurmjobir.PodGroup)
			second.Name, second.UID, second.ResourceVersion = "second", "second", ""
			if err := sb.Create(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			for i, pod := range pods {
				if err := sb.Get(t.Context(), client.ObjectKeyFromObject(pod), pod); err != nil {
					t.Fatal(err)
				}
				pod.Labels[wellknown.LabelExternalHetJobId] = "10"
				if i == 1 {
					pod.Spec.SchedulingGroup.PodGroupName = ptr.To("second")
					pod.Labels[wellknown.LabelExternalJobId] = "20"
				}
				if err := sb.Update(t.Context(), pod); err != nil {
					t.Fatal(err)
				}
			}
			manager := internalcache.New(t.Context(), nil, true, true)
			manager.AddCompositePodGroup(klog.FromContext(t.Context()), root)
			info := &framework.PodGroupInfo{Namespace: "test", Name: "root", Type: fwk.CompositePodGroupKeyType, CompositePodGroup: root}
			for i, pg := range []*slurmjobir.PodGroup{first, second} {
				native := &schedulingv1beta1.PodGroup{ObjectMeta: pg.ObjectMeta, Spec: schedulingv1beta1.PodGroupSpec{
					ParentCompositePodGroupName: ptr.To("root"), SchedulingPolicy: pg.Spec.SchedulingPolicy,
				}}
				manager.AddPodGroup(native)
				manager.AddPodGroupMember(pods[i])
				info.Children = append(info.Children, &framework.PodGroupInfo{Namespace: "test", Name: pg.Name, Type: fwk.PodGroupKeyType, PodGroup: native, UnscheduledPods: []*corev1.Pod{pods[i]}})
			}
			sb.handle = nativeManagerHandle{Framework: sb.handle.(framework.Framework), manager: manager, bridge: sb}
			control := &nativeHetControl{postFilterSlurmControl: postFilterSlurmControl{nodeNamesByPartition: map[string][]string{"": {"node1", "node2"}}}}
			sb.slurmControl = control
			rootState := framework.NewCycleState()
			if !unevaluated {
				parentPlacement := framework.NewCycleState()
				parentPlacement.SetPodGroupSchedulingCycle(rootState)
				for _, pod := range pods {
					child := framework.NewCycleState()
					child.SetPlacementCycleState(parentPlacement)
					if result, status := sb.PreFilter(t.Context(), nativePodState(child), pod, nil); !status.IsSuccess() || result != nil {
						t.Fatalf("PreFilter: %v %v", result, status)
					}
				}
				if control.reads != 1 {
					t.Fatalf("composite allocation discovery repeated %d times", control.reads)
				}
			}
			if _, status := sb.PodGroupPostFilter(t.Context(), rootState, info, nil); !status.IsSuccess() {
				t.Fatalf("PostFilter: %v", status)
			}
			if lists.Load() != 1 {
				t.Fatalf("membership lists=%d, want 1", lists.Load())
			}
			want := []string{"update:10", "update:20", "release:10", "release:20"}
			if !reflect.DeepEqual(control.effects, want) {
				t.Fatalf("effects=%v, want %v", control.effects, want)
			}
			data, _ := rootState.Read(nativeGroupCycleKey)
			group := data.(*nativeGroupCycle)
			if len(group.members) != 2 {
				t.Fatalf("recorded %d members, want 2", len(group.members))
			}
			for _, allocation := range group.allocations {
				for _, component := range allocation.ir.Components {
					if len(component.JobInfo.ExcNodes) != 0 {
						t.Fatal("PostFilter mutated shared translation")
					}
				}
			}
		})
	}
}

func TestNativeGangSharesValidationAcross2048Bindings(t *testing.T) {
	sb, control, _, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
	const size = 2048
	ir := &slurmjobir.SlurmJobIR{Components: make([]slurmjobir.SlurmJobComponent, 1)}
	ir.Components[0].Pods.Items = make([]corev1.Pod, size)
	a := &nativeAllocation{ir: ir, jobs: []slurmcontrol.ExternalJob{control.job},
		components: make([]nativeComponent, 1), members: make(map[client.ObjectKey]nativeMember)}
	// Sharing is per allocation; every Pod in this fixture uses the same Node
	// object so the test isolates concurrent validation from placement setup.
	a.components[0].slurmNodes = []string{"node1"}
	for i := range size {
		pod := allocationPod(fmt.Sprintf("pod-%d", i), 0, "10", "")
		ir.Components[0].Pods.Items[i] = *pod
		a.members[client.ObjectKeyFromObject(pod)] = nativeMember{pod: &ir.Components[0].Pods.Items[i]}
	}
	var bindings sync.WaitGroup
	for i := range size {
		bindings.Go(func() {
			pod := &ir.Components[0].Pods.Items[i]
			state := framework.NewCycleState()
			state.Write(stateKey, &stateData{native: a, slurmJobIR: ir, allocation: a.jobs, nodeName: "node1"})
			if status := sb.PreBind(t.Context(), state, pod, "node1"); !status.IsSuccess() {
				t.Errorf("binding: %v", status)
			}
		})
	}
	bindings.Wait()
	if control.allocationReads.Load() != 1 || control.liveReads.Load() != 1 || control.resourceReads.Load() != 1 {
		t.Fatalf("allocation=%d live=%d resources=%d; want one each", control.allocationReads.Load(), control.liveReads.Load(), control.resourceReads.Load())
	}
}

func TestNativeBindingValidationFailureAndRetry(t *testing.T) {
	for _, change := range []string{"duplicate", "replacement", "cancelled", "lost node", "live error"} {
		t.Run(change, func(t *testing.T) {
			sb, control, pods, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
			group := framework.NewCycleState()
			states := []*framework.CycleState{nativePodState(group), nativePodState(group)}
			for i, pod := range pods {
				if _, status := sb.PreFilter(t.Context(), states[i], pod, nil); !status.IsSuccess() {
					t.Fatal(status)
				}
			}
			original := control.job
			control.liveJob = ptr.To(control.job)
			switch change {
			case "duplicate":
				control.allocationErr = slurmjob.ErrAmbiguousAllocation
			case "replacement":
				control.job.JobId = 11
			case "cancelled":
				control.liveJob.Finished = true
			case "lost node":
				control.liveJob.Nodes = "node1"
			case "live error":
				control.liveErr = errors.New("Slurm unavailable")
			}
			for i, pod := range pods {
				if status := sb.PreBind(t.Context(), states[i], pod, fmt.Sprintf("node%d", i+1)); status.IsSuccess() {
					t.Fatal("invalid allocation bound")
				}
			}
			if control.allocationReads.Load() != 2 || control.liveReads.Load() > 1 || control.resourceReads.Load() != 0 {
				t.Fatalf("failed validation repeated or fetched resources: %d %d %d", control.allocationReads.Load(), control.liveReads.Load(), control.resourceReads.Load())
			}
			control.job, control.liveJob, control.liveErr, control.allocationErr = original, nil, nil, nil
			next := nativePodState(framework.NewCycleState())
			if _, status := sb.PreFilter(t.Context(), next, pods[0], nil); !status.IsSuccess() {
				t.Fatal(status)
			}
			if status := sb.PreBind(t.Context(), next, pods[0], "node1"); !status.IsSuccess() {
				t.Fatal(status)
			}
		})
	}
}

func TestNativeBindingChecksEachMemberAfterSharedValidation(t *testing.T) {
	for _, change := range []string{"uid", "job", "hetjob", "node"} {
		t.Run(change, func(t *testing.T) {
			sb, control, pods, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
			group := framework.NewCycleState()
			states := []*framework.CycleState{nativePodState(group), nativePodState(group)}
			for i, pod := range pods {
				if _, status := sb.PreFilter(t.Context(), states[i], pod, nil); !status.IsSuccess() {
					t.Fatal(status)
				}
			}
			if status := sb.PreBind(t.Context(), states[0], pods[0], "node1"); !status.IsSuccess() {
				t.Fatal(status)
			}
			changed := pods[1].DeepCopy()
			node := "node2"
			switch change {
			case "uid":
				changed.UID = "replacement"
			case "job":
				changed.Labels[wellknown.LabelExternalJobId] = "11"
			case "hetjob":
				changed.Labels[wellknown.LabelExternalHetJobId] = "11"
			case "node":
				node = "node1"
			}
			if status := sb.PreBind(t.Context(), states[1], changed, node); status.Code() != fwk.Unschedulable {
				t.Fatalf("changed member accepted: %v", status)
			}
			if control.liveReads.Load() != 1 {
				t.Fatal("member validation made another live read")
			}
		})
	}
}
