// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestCompositeLateChildDoesNotBlockSubmittedBatch(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, published := range []bool{false, true} {
			t.Run(fmt.Sprintf("nested-%t/published-%t", nested, published), func(t *testing.T) {
				ctx := t.Context()
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
				root := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "scheduling.k8s.io/v1alpha3", "kind": "CompositePodGroup",
					"metadata": map[string]any{"namespace": "ns", "name": "root", "uid": "root-uid"},
					"spec":     map[string]any{"schedulingPolicy": map[string]any{"gang": map[string]any{"minGroupCount": int64(1)}}},
				}}
				objects := []client.Object{root}
				parent := root.GetName()
				if nested {
					child := root.DeepCopy()
					child.SetName("nested")
					child.SetUID("nested-uid")
					child.Object["spec"].(map[string]any)["parentCompositePodGroupName"] = root.GetName()
					objects = append(objects, child)
					parent = child.GetName()
				}
				member := func(name string) (*slurmjobir.PodGroup, *corev1.Pod) {
					group := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta,
						ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name)},
						Spec:       slurmjobir.PodGroupSpec{ParentCompositePodGroupName: ptr.To(parent), SchedulingPolicy: schedulingv1.PodGroupSchedulingPolicy{Gang: &schedulingv1.GangSchedulingPolicy{MinCount: 1}}}}
					pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name + "-pod")},
						Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(name)},
							SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}, {Name: "other/gate"}},
							Containers:      []corev1.Container{{Name: "worker", Image: "busybox"}}}}
					return group, pod
				}
				firstGroup, first := member("first")
				objects = append(objects, firstGroup, first)
				kube := kubefake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithObjects(objects...).Build()
				live := slurmfake.NewFakeClient()
				submissions := int32(0)
				var canceled []int32
				slurm := interceptor.NewClient(live, interceptor.Funcs{
					Create: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.CreateOption) error {
						desc := req.(api.V0044JobSubmitReq).Job
						if desc == nil {
							t.Fatal("expected one represented component")
						}
						job := obj.(*api.V0044JobInfo)
						job.JobId, job.AdminComment = ptr.To(100+10*submissions), desc.AdminComment
						job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING})
						submissions++
						return live.Create(ctx, job, nil)
					},
					Delete: func(ctx context.Context, obj object.Object, opts ...slurmclient.DeleteOption) error {
						job := &api.V0044JobInfo{}
						if err := live.Get(ctx, obj.GetKey(), job); err != nil {
							return err
						}
						canceled = append(canceled, *job.JobId)
						job.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED})
						return live.Update(ctx, job, nil)
					},
				})
				recorder := record.NewFakeRecorder(20)
				// Rebuild the controller and informer on each step: no in-memory batch state survives.
				reconcile := func() {
					t.Helper()
					raw := &api.V0044JobInfoObjectList{}
					if err := live.List(ctx, raw); err != nil {
						t.Fatal(err)
					}
					var cached []object.RuntimeObject
					for i := range raw.Items {
						cached = append(cached, raw.Items[i].DeepCopy())
					}
					informer := &registrationInformer{InformerCache: slurmfake.NewFakeClient(cached...).GetInformer(api.ObjectTypeV0044JobInfo), synced: true}
					r := NewReconciler(kube, slurm, "slurm-bridge", dra.DefaultRegistry(), workloadAPI, "", "")
					r.apiReader, r.informer, r.enqueue = kube, informer, func(Request) {}
					r.eventRecorder, r.eventHistory = recorder, cache.NewExpiring()
					r.jobIndex, err = slurmjob.NewPodIndex(slurm, informer)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := r.Reconcile(ctx, objectRequest(root)); err != nil {
						t.Fatal(err)
					}
				}
				reconcile()
				if submissions != 1 {
					t.Fatalf("submissions=%d, want quorum submission", submissions)
				}
				if published {
					reconcile()
				}
				lateGroup, late := member("late")
				for _, obj := range []client.Object{lateGroup, late} {
					if err := kube.Create(ctx, obj); err != nil {
						t.Fatal(err)
					}
				}
				reconcile()
				for _, pod := range []*corev1.Pod{first, late} {
					if err := kube.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
						t.Fatal(err)
					}
				}
				if first.Labels[wellknown.LabelExternalJobId] != "100" || hasRegistrationGate(first) || first.Spec.NodeName != "" {
					t.Fatalf("original unbound member lost publication: %+v", first)
				}
				if !hasRegistrationGate(late) || late.Labels[wellknown.LabelExternalJobId] != "" || submissions != 1 {
					t.Fatalf("late member adopted active allocation: %+v; submissions=%d", late, submissions)
				}
				select {
				case event := <-recorder.Events:
					if !strings.Contains(event, "WaitingForNextAllocation") {
						t.Fatalf("unexpected event: %s", event)
					}
				default:
					t.Fatal("missing late-arrival event")
				}
				original := &api.V0044JobInfo{}
				if err := live.Get(ctx, "100", original); err != nil {
					t.Fatal(err)
				}
				original.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING})
				if err := live.Update(ctx, original, nil); err != nil {
					t.Fatal(err)
				}
				duplicate := original.DeepCopy()
				duplicate.JobId = ptr.To(int32(200))
				duplicate.JobState = ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING})
				if err := live.Create(ctx, duplicate, nil); err != nil {
					t.Fatal(err)
				}
				reconcile()
				if !slices.Equal(canceled, []int32{200}) {
					t.Fatalf("duplicate cleanup canceled %v", canceled)
				}
				first.Status.Phase = corev1.PodSucceeded
				if err := kube.Status().Update(ctx, first); err != nil {
					t.Fatal(err)
				}
				for range 4 {
					reconcile()
				}
				if err := kube.Get(ctx, client.ObjectKeyFromObject(late), late); err != nil {
					t.Fatal(err)
				}
				if submissions != 2 || late.Labels[wellknown.LabelExternalJobId] != "110" || hasRegistrationGate(late) || !slices.Contains(canceled, 100) {
					t.Fatalf("next batch did not progress: submissions=%d canceled=%v pod=%+v", submissions, canceled, late)
				}
			})
		}
	}
}
