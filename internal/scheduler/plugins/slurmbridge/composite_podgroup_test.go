// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	fwk "k8s.io/kube-scheduler/framework"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	tf "k8s.io/kubernetes/pkg/scheduler/testing/framework"
	"k8s.io/utils/ptr"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type compositePostFilterHandle struct {
	framework.Framework
	preFilter   func(context.Context, fwk.CycleState, *corev1.Pod) (*fwk.PreFilterResult, *fwk.Status, sets.Set[string])
	filterError bool
}

func (h compositePostFilterHandle) RunPreFilterPlugins(ctx context.Context, state fwk.CycleState, pod *corev1.Pod) (*fwk.PreFilterResult, *fwk.Status, sets.Set[string]) {
	return h.preFilter(ctx, state, pod)
}

func (h compositePostFilterHandle) RunFilterPluginsWithNominatedPods(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, node fwk.NodeInfo) *fwk.Status {
	if h.filterError {
		return fwk.NewStatus(fwk.Error, "filter failed")
	}
	return h.RunFilterPlugins(ctx, state, pod, node)
}

type compositeUpdateRecorder struct {
	slurmcontrol.SlurmControlInterface
	roots      []string
	components []int
	failRoot   string
	failErr    error
}

func (r *compositeUpdateRecorder) GetJob(_ context.Context, pod *corev1.Pod) (*slurmcontrol.ExternalJob, error) {
	return &slurmcontrol.ExternalJob{JobId: slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId]), Pending: true, Held: true}, nil
}

func (r *compositeUpdateRecorder) GetAllocation(_ context.Context, _ *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	return testAllocation(ir), nil
}

func (r *compositeUpdateRecorder) ReleaseJob(context.Context, int32, *int32) error {
	return nil
}

func (r *compositeUpdateRecorder) GetNodeNames(context.Context, *string) ([]string, error) {
	return []string{"node"}, nil
}

func (r *compositeUpdateRecorder) UpdateJob(_ context.Context, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) (int32, error) {
	if ir.RootPOM.Name == r.failRoot {
		return 0, r.failErr
	}
	if ir.ComponentOf(pod.Namespace, pod.Name) == len(ir.Components)-1 {
		r.roots = append(r.roots, ir.RootPOM.Name)
		r.components = append(r.components, len(ir.Components))
	}
	return slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId]), nil
}

func TestPodGroupPostFilterCompositeAllocations(t *testing.T) {
	metrics.Register()
	tests := []struct {
		name                                                                    string
		rootGang, nestedGang, nestedBasic, unevaluated, filterError, firstReady bool
		preFilterStatus                                                         *fwk.Status
		updateErr                                                               error
		wantRoots                                                               []string
		wantComponents                                                          []int
		wantCode                                                                fwk.Code
	}{
		{name: "independent children both update", wantRoots: []string{"a0", "b"}, wantComponents: []int{1, 1}},
		{name: "rejected child does not block sibling", preFilterStatus: fwk.NewStatus(fwk.Unschedulable), wantRoots: []string{"b"}, wantComponents: []int{1}},
		{name: "unevaluated rejected child does not block sibling", unevaluated: true, preFilterStatus: fwk.NewStatus(fwk.UnschedulableAndUnresolvable), wantRoots: []string{"b"}, wantComponents: []int{1}},
		{name: "unschedulable update does not block sibling", updateErr: errors.New("ESLURM_JOB_NOT_PENDING"), wantRoots: []string{"b"}, wantComponents: []int{1}},
		{name: "nested gang updates once with all components", nestedGang: true, wantRoots: []string{"gang", "b"}, wantComponents: []int{2, 1}},
		{name: "gang pending applies only to its allocation", nestedGang: true, firstReady: true, wantRoots: []string{"gang", "b"}, wantComponents: []int{2, 1}},
		{name: "pending sibling does not update ready allocation", firstReady: true, wantRoots: []string{"b"}, wantComponents: []int{1}},
		{name: "rejected gang component blocks only its allocation", nestedGang: true, preFilterStatus: fwk.NewStatus(fwk.Unschedulable), wantRoots: []string{"b"}, wantComponents: []int{1}},
		{name: "gang root retains one allocation", rootGang: true, wantRoots: []string{"root"}, wantComponents: []int{2}},
		{name: "gang root cannot update rejected component", rootGang: true, preFilterStatus: fwk.NewStatus(fwk.Unschedulable), wantCode: fwk.Unschedulable},
		{name: "basic child separates allocations below gang", rootGang: true, nestedBasic: true, wantRoots: []string{"a0", "a1", "root"}, wantComponents: []int{1, 1, 1}},
		{name: "prefilter error propagates", preFilterStatus: fwk.NewStatus(fwk.Error, "prefilter failed"), wantCode: fwk.Error},
		{name: "filter error propagates", filterError: true, wantCode: fwk.Error},
		{name: "update error propagates", updateErr: errors.New("Slurm unavailable"), wantCode: fwk.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := schedulingv1alpha3.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			api := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
			root := &schedulingv1alpha3.CompositePodGroup{
				TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha3", Kind: "CompositePodGroup"},
				ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: "test", UID: "root"},
				Spec:       schedulingv1alpha3.CompositePodGroupSpec{SchedulingPolicy: schedulingv1alpha3.CompositePodGroupSchedulingPolicy{Basic: &schedulingv1alpha3.CompositeBasicSchedulingPolicy{}}},
			}
			if tt.rootGang {
				root.Spec.SchedulingPolicy = schedulingv1alpha3.CompositePodGroupSchedulingPolicy{Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 2}}
			}
			info := &framework.PodGroupInfo{Namespace: "test", Name: root.Name, Type: fwk.CompositePodGroupKeyType, CompositePodGroup: root}
			objects := []kubeclient.Object{root}
			parent := info
			if tt.nestedGang || tt.nestedBasic {
				gang := root.DeepCopy()
				gang.Name, gang.UID = "gang", "gang"
				gang.Spec.ParentCompositePodGroupName = ptr.To(root.Name)
				gang.Spec.SchedulingPolicy = schedulingv1alpha3.CompositePodGroupSchedulingPolicy{Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 2}}
				if tt.nestedBasic {
					gang.Spec.SchedulingPolicy = schedulingv1alpha3.CompositePodGroupSchedulingPolicy{Basic: &schedulingv1alpha3.CompositeBasicSchedulingPolicy{}}
				}
				parent = &framework.PodGroupInfo{Namespace: "test", Name: gang.Name, Type: fwk.CompositePodGroupKeyType, CompositePodGroup: gang}
				info.Children = append(info.Children, parent)
				objects = append(objects, gang)
			}
			names := []string{"a0", "b"}
			if tt.nestedGang || tt.nestedBasic {
				names = []string{"a0", "a1", "b"}
			}
			for i, name := range names {
				pgParent := parent
				if name == "b" {
					pgParent = info
				}
				pg := &slurmjobir.PodGroup{
					TypeMeta:   api.PodGroupTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name)},
					Spec:       slurmjobir.PodGroupSpec{ParentCompositePodGroupName: ptr.To(pgParent.Name), SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1}}},
				}
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name), Labels: map[string]string{wellknown.LabelExternalJobId: strconv.Itoa(10 * (i + 1))}}, Spec: corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(name)}}}
				if tt.firstReady && name == "a0" {
					pod.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node"}
				}
				pgParent.Children = append(pgParent.Children, &framework.PodGroupInfo{Namespace: "test", Name: name, Type: fwk.PodGroupKeyType, PodGroup: &schedulingv1beta1.PodGroup{}, UnscheduledPods: []*corev1.Pod{pod}})
				objects = append(objects, pg, pod)
			}
			cl := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			f, err := tf.NewFramework(ctx, []tf.RegisterPluginFunc{
				tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
				tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
				tf.RegisterFilterPlugin(Name, func(context.Context, runtime.Object, fwk.Handle) (fwk.Plugin, error) { return &SlurmBridge{}, nil }),
			}, "slurm-bridge", fwkruntime.WithInformerFactory(informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0)),
				fwkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(nil, []*corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node"}}})),
				fwkruntime.WithPodActivator(&activateRecorder{}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			prepare := func(ctx context.Context, state fwk.CycleState, pod *corev1.Pod) (*fwk.PreFilterResult, *fwk.Status, sets.Set[string]) {
				if pod.Name == "a0" && tt.preFilterStatus != nil {
					return nil, tt.preFilterStatus, nil
				}
				ir, err := slurmjobir.TranslateToSlurmJobIR(cl, nil, api, ctx, pod)
				if err != nil {
					return nil, fwk.AsStatus(err), nil
				}
				state.Write(stateKey, &stateData{slurmJobIR: ir})
				if pod.Name == "a0" && tt.firstReady {
					return &fwk.PreFilterResult{NodeNames: sets.New("node")}, nil, nil
				}
				return nil, nil, nil
			}
			control := &compositeUpdateRecorder{failRoot: "a0", failErr: tt.updateErr}
			if tt.updateErr == nil {
				control.failRoot = ""
			}
			sb := &SlurmBridge{Client: cl, workloadAPI: api, slurmControl: control, handle: compositePostFilterHandle{Framework: f, preFilter: prepare, filterError: tt.filterError}}
			state := framework.NewCycleState()
			if !tt.unevaluated {
				group := &nativeGroupCycle{}
				for _, pod := range info.GetUnscheduledPods() {
					podState := framework.NewCycleState()
					result, status, _ := prepare(ctx, podState, pod)
					group.members = append(group.members, nativeGroupMemberCycle{state: podState, pod: pod, result: result, status: status})
				}
				state.Write(nativeGroupCycleKey, group)
			}
			result, status := sb.PodGroupPostFilter(ctx, state, info, nil)
			if status.Code() != tt.wantCode {
				t.Fatalf("PodGroupPostFilter(%s) = %v, want code %v", tt.name, status, tt.wantCode)
			}
			if status.IsSuccess() && result == nil {
				t.Fatal("successful PodGroupPostFilter returned nil result")
			}
			if !reflect.DeepEqual(control.roots, tt.wantRoots) || !reflect.DeepEqual(control.components, tt.wantComponents) {
				t.Fatalf("Slurm updates = %v with components %v, want %v with components %v", control.roots, control.components, tt.wantRoots, tt.wantComponents)
			}
			for _, pod := range info.GetUnscheduledPods() {
				stored := &corev1.Pod{}
				if err := cl.Get(ctx, kubeclient.ObjectKeyFromObject(pod), stored); err != nil {
					t.Fatal(err)
				}
				if stored.Labels[wellknown.LabelExternalJobId] != pod.Labels[wellknown.LabelExternalJobId] {
					t.Errorf("scheduler changed registration metadata for pod %s", pod.Name)
				}
			}
			if status.IsSuccess() {
				if _, retryStatus := sb.PodGroupPostFilter(ctx, state, info, nil); !retryStatus.IsSuccess() {
					t.Fatal(retryStatus)
				}
				if !reflect.DeepEqual(control.roots, tt.wantRoots) {
					t.Errorf("repeated PostFilter repeated allocation updates: %v", control.roots)
				}
			}
		})
	}
}
