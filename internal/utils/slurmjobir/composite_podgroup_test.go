// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newSchedulingV1Alpha3Object(kind, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": workloadAPIGroup + "/v1alpha3",
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": corev1.NamespaceDefault,
		},
		"spec": spec,
	}}
}

func TestTranslateToSlurmJobIRSelectsCompositePodGroupRoot(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	workloadAPI := mustRegisterWorkloadAPI(t, scheme, WorkloadAPIVersionV1Beta1)

	const (
		leafName     = "leaf"
		parentName   = "parent"
		rootName     = "root"
		workloadName = "workload"
	)
	pod := podWithSchedulingGroup(corev1.NamespaceDefault, "pod", leafName)
	leaf := newPodGroup(leafName, corev1.NamespaceDefault, schedulingv1beta1.PodGroupSchedulingPolicy{
		Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1},
	})
	leaf.Spec.ParentCompositePodGroupName = ptr.To(parentName)
	leaf.Spec.WorkloadRef = &WorkloadReference{TemplateName: leafName, WorkloadName: workloadName}
	parent := newSchedulingV1Alpha3Object("CompositePodGroup", parentName, map[string]any{
		"parentCompositePodGroupName": rootName,
	})
	root := newSchedulingV1Alpha3Object("CompositePodGroup", rootName, map[string]any{
		"workloadRef": map[string]any{"workloadName": workloadName, "templateName": rootName},
	})
	workload := newSchedulingV1Alpha3Object("Workload", workloadName, map[string]any{
		"compositePodGroupTemplates": []any{map[string]any{
			"name": rootName,
			"compositePodGroupTemplates": []any{map[string]any{
				"name": parentName,
				"podGroupTemplates": []any{map[string]any{
					"name":             leafName,
					"schedulingPolicy": map[string]any{"gang": map[string]any{"minCount": int64(1)}},
				}},
			}},
		}},
	})
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, leaf, parent, root, workload).Build()
	got, err := TranslateToSlurmJobIR(kubeClient, nil, workloadAPI, context.Background(), pod)
	if err != nil {
		t.Fatalf("TranslateToSlurmJobIR() error = %v, want nil", err)
	}
	if got.RootPOM.TypeMeta != compositePodGroupV1Alpha3 || got.RootPOM.Name != rootName {
		t.Errorf("TranslateToSlurmJobIR() root = %v %q, want %v %q", got.RootPOM.TypeMeta, got.RootPOM.Name, compositePodGroupV1Alpha3, rootName)
	}
}

func Test_translator_fromCompositePodGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	workloadAPI := mustRegisterWorkloadAPI(t, scheme, WorkloadAPIVersionV1Beta1)

	const (
		rootName       = "root"
		childName      = "child"
		grandChildName = "grandchild"
		workloadName   = "workload"
	)
	rootPOM := &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{Name: rootName, Namespace: corev1.NamespaceDefault},
	}
	root := newSchedulingV1Alpha3Object("CompositePodGroup", rootName, map[string]any{})
	child := newSchedulingV1Alpha3Object("CompositePodGroup", childName, map[string]any{
		"parentCompositePodGroupName": rootName,
	})
	grandchild := newSchedulingV1Alpha3Object("CompositePodGroup", "grandchild", map[string]any{
		"parentCompositePodGroupName": childName,
	})

	runtimePodGroup := func(name, parentName string) *PodGroup {
		pg := newPodGroup(name, corev1.NamespaceDefault, schedulingv1beta1.PodGroupSchedulingPolicy{})
		pg.Spec.ParentCompositePodGroupName = ptr.To(parentName)
		return pg
	}
	flatGroup := runtimePodGroup("flat-runtime", rootName)
	nestedGroup := runtimePodGroup("nested-runtime", grandChildName)
	flatPod := podWithSchedulingGroup(corev1.NamespaceDefault, "flat-pod", flatGroup.Name)
	nestedPod0 := podWithSchedulingGroup(corev1.NamespaceDefault, "nested-pod0", nestedGroup.Name)
	nestedPod1 := podWithSchedulingGroup(corev1.NamespaceDefault, "nested-pod1", nestedGroup.Name)

	tests := []struct {
		name    string
		reader  client.Reader
		want    *SlurmJobIR
		wantErr bool
	}{
		{
			name:    "CompositePodGroup does not exist",
			reader:  fake.NewClientBuilder().WithScheme(scheme).Build(),
			wantErr: true,
		},
		{
			name: "PodGroup list fails",
			reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				root,
				flatGroup,
			).WithInterceptorFuncs(interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					return errors.New("list PodGroups")
				},
			}).Build(),
			want:    nil,
			wantErr: true,
		},
		{
			name: "flat and nested templates",
			reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				root,
				child,
				grandchild,
				flatGroup,
				nestedGroup,
				flatPod,
				nestedPod0,
				nestedPod1,
			).Build(),
			want: &SlurmJobIR{Components: []SlurmJobComponent{
				{
					ObjectMeta: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:            flatGroup.Name,
							Namespace:       corev1.NamespaceDefault,
							ResourceVersion: "999",
						},
					},
					JobInfo: SlurmJobIRJobInfo{
						JobName:      ptr.To("flat-runtime"),
						MinNodes:     ptr.To(int32(1)),
						MaxNodes:     ptr.To(int32(1)),
						TasksPerNode: ptr.To(int32(1)),
					},
					Pods: corev1.PodList{Items: []corev1.Pod{*flatPod}},
				},
				{
					ObjectMeta: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:            nestedGroup.Name,
							Namespace:       corev1.NamespaceDefault,
							ResourceVersion: "999",
						},
					},
					JobInfo: SlurmJobIRJobInfo{
						JobName:      ptr.To("nested-runtime"),
						MinNodes:     ptr.To(int32(2)),
						MaxNodes:     ptr.To(int32(2)),
						TasksPerNode: ptr.To(int32(1)),
					},
					Pods: corev1.PodList{Items: []corev1.Pod{*nestedPod0, *nestedPod1}},
				},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := translator{Reader: tt.reader, ctx: context.Background(), workloadAPI: workloadAPI}
			got, err := tr.fromCompositePodGroup(&corev1.Pod{}, rootPOM)
			if (err != nil) != tt.wantErr {
				t.Errorf("translator.fromCompositePodGroup() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("translator.fromCompositePodGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}

type readinessHandle struct {
	fwk.Handle
	manager fwk.PodGroupManager
}

func (h readinessHandle) PodGroupManager() fwk.PodGroupManager { return h.manager }

type readinessSnapshot struct {
	fwk.PodGroupManager
	groups     map[string]*schedulingv1beta1.PodGroup
	counts     map[string]int
	composites map[string]*schedulingv1alpha3.CompositePodGroup
	children   map[string][]fwk.EntityKey
	root       fwk.EntityKey
	found      bool
	buildErr   error
	rootErr    error
}

func (s *readinessSnapshot) BuildHierarchySnapshotFromPod(*corev1.Pod) (fwk.PodGroupManager, error) {
	return s, s.buildErr
}
func (s *readinessSnapshot) GetRootKeyForGroup(fwk.EntityKey) (fwk.EntityKey, bool, error) {
	return s.root, s.found, s.rootErr
}
func (s *readinessSnapshot) PodGroups() fwk.PodGroupLister { return readinessGroupLister(s.groups) }
func (s *readinessSnapshot) PodGroupStates() fwk.PodGroupStateLister {
	return readinessGroupStateLister(s.counts)
}
func (s *readinessSnapshot) CompositePodGroups() fwk.CompositePodGroupLister {
	return readinessCompositeLister(s.composites)
}
func (s *readinessSnapshot) CompositePodGroupStates() fwk.CompositePodGroupStateLister {
	return readinessCompositeStateLister(s.children)
}

type readinessGroupLister map[string]*schedulingv1beta1.PodGroup

func (l readinessGroupLister) Get(_, name string) (*schedulingv1beta1.PodGroup, error) {
	if group := l[name]; group != nil {
		return group, nil
	}
	return nil, fmt.Errorf("pod group %q missing", name)
}

type readinessGroupStateLister map[string]int

func (l readinessGroupStateLister) Get(_, name string) (fwk.PodGroupState, error) {
	if count, ok := l[name]; ok {
		return readinessGroupState{count: count}, nil
	}
	return nil, fmt.Errorf("pod group state %q missing", name)
}

type readinessGroupState struct {
	fwk.PodGroupState
	count int
}

func (s readinessGroupState) AllPodsCount() int { return s.count }

type readinessCompositeLister map[string]*schedulingv1alpha3.CompositePodGroup

func (l readinessCompositeLister) Get(_, name string) (*schedulingv1alpha3.CompositePodGroup, error) {
	if group := l[name]; group != nil {
		return group, nil
	}
	return nil, fmt.Errorf("composite pod group %q missing", name)
}

type readinessCompositeStateLister map[string][]fwk.EntityKey

func (l readinessCompositeStateLister) Get(_, name string) (fwk.CompositePodGroupState, error) {
	if children, ok := l[name]; ok {
		return readinessCompositeState{children: children}, nil
	}
	return nil, fmt.Errorf("composite pod group state %q missing", name)
}

type readinessCompositeState struct {
	fwk.CompositePodGroupState
	children []fwk.EntityKey
}

func (s readinessCompositeState) GetChildren() []fwk.EntityKey { return s.children }

func Test_translator_PreFilterCompositePodGroup(t *testing.T) {
	const ns = corev1.NamespaceDefault
	newSnapshot := func() *readinessSnapshot {
		return &readinessSnapshot{
			groups: map[string]*schedulingv1beta1.PodGroup{
				"leaf": {Spec: schedulingv1beta1.PodGroupSpec{ParentCompositePodGroupName: ptr.To("child")}},
			},
			counts: map[string]int{"leaf": 1},
			composites: map[string]*schedulingv1alpha3.CompositePodGroup{
				"root": {Spec: schedulingv1alpha3.CompositePodGroupSpec{
					SchedulingPolicy: schedulingv1alpha3.CompositePodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 2},
					},
				}},
				"child": {},
			},
			children: map[string][]fwk.EntityKey{
				"root":  {fwk.CompositePodGroupKey(ns, "child"), fwk.PodGroupKey(ns, "other")},
				"child": {fwk.PodGroupKey(ns, "leaf")},
			},
			root: fwk.CompositePodGroupKey(ns, "root"), found: true,
		}
	}

	waiting := fwk.NewStatus(fwk.UnschedulableAndUnresolvable, `waiting for composite pod group "root" tree to meet quorum`)
	lookupErr := errors.New("lookup failed")
	tests := []struct {
		name   string
		change func(*readinessSnapshot)
		want   *fwk.Status
	}{
		{name: "nested child ready but root gang short", want: waiting},
		{name: "root gang met", change: func(s *readinessSnapshot) {
			s.groups["other"] = &schedulingv1beta1.PodGroup{}
			s.counts["other"] = 1
		}},
		{name: "nested basic child short", change: func(s *readinessSnapshot) { s.counts["leaf"] = 0 }, want: waiting},
		{name: "gang leaf short", change: func(s *readinessSnapshot) {
			s.groups["leaf"].Spec.SchedulingPolicy.Gang = &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}
			s.groups["other"] = &schedulingv1beta1.PodGroup{}
			s.counts["other"] = 1
		}, want: waiting},
		{name: "gang leaf met", change: func(s *readinessSnapshot) {
			s.groups["leaf"].Spec.SchedulingPolicy.Gang = &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}
			s.counts["leaf"] = 2
			s.groups["other"] = &schedulingv1beta1.PodGroup{}
			s.counts["other"] = 1
		}},
		{name: "nested gang child short", change: func(s *readinessSnapshot) {
			s.composites["child"].Spec.SchedulingPolicy.Gang = &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 2}
			s.groups["other"] = &schedulingv1beta1.PodGroup{}
			s.counts["other"] = 1
		}, want: waiting},
		{name: "nested gang child met", change: func(s *readinessSnapshot) {
			s.composites["child"].Spec.SchedulingPolicy.Gang = &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 2}
			s.children["child"] = append(s.children["child"], fwk.PodGroupKey(ns, "second"))
			s.groups["second"] = &schedulingv1beta1.PodGroup{}
			s.counts["second"] = 1
			s.groups["other"] = &schedulingv1beta1.PodGroup{}
			s.counts["other"] = 1
		}},
		{name: "basic root needs one child", change: func(s *readinessSnapshot) {
			s.composites["root"].Spec.SchedulingPolicy.Gang = nil
		}},
		{name: "snapshot error", change: func(s *readinessSnapshot) {
			s.buildErr = errors.New("snapshot failed")
		}, want: fwk.NewStatus(fwk.UnschedulableAndUnresolvable, "failed to build hierarchy snapshot: snapshot failed")},
		{name: "leaf object missing", change: func(s *readinessSnapshot) {
			delete(s.groups, "leaf")
		}, want: fwk.NewStatus(fwk.UnschedulableAndUnresolvable, `waiting for pods's pod group "leaf" to appear in scheduling queue`)},
		{name: "root missing", change: func(s *readinessSnapshot) {
			s.found = false
		}, want: fwk.NewStatus(fwk.UnschedulableAndUnresolvable, "failed to build hierarchy snapshot: composite pod group object not found in state for compositepodgroup/default/child")},
		{name: "root lookup error", change: func(s *readinessSnapshot) {
			s.rootErr = lookupErr
		}, want: fwk.AsStatus(lookupErr)},
		{name: "root object missing", change: func(s *readinessSnapshot) {
			delete(s.composites, "root")
		}, want: waiting},
		{name: "root state missing", change: func(s *readinessSnapshot) {
			delete(s.children, "root")
		}, want: waiting},
		{name: "nested state missing", change: func(s *readinessSnapshot) {
			delete(s.children, "child")
		}, want: waiting},
		{name: "leaf state missing", change: func(s *readinessSnapshot) {
			delete(s.counts, "leaf")
		}, want: waiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := newSnapshot()
			if tt.change != nil {
				tt.change(snapshot)
			}
			tr := translator{handle: readinessHandle{manager: snapshot}}
			got := tr.PreFilterCompositePodGroup(podWithSchedulingGroup(ns, "pod", "leaf"), &SlurmJobIR{})
			if !got.Equal(tt.want) {
				t.Errorf("PreFilterCompositePodGroup(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
	if got := (&translator{}).PreFilterCompositePodGroup(&corev1.Pod{}, &SlurmJobIR{}); got != nil {
		t.Errorf("PreFilterCompositePodGroup(pod without scheduling group) = %v, want nil", got)
	}
}
