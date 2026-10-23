// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	schedulercontrol "github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestRegisterPodGroupNextBatch(t *testing.T) {
	for _, version := range []string{slurmjobir.WorkloadAPIVersionV1Alpha2, slurmjobir.WorkloadAPIVersionV1Beta1} {
		for _, pending := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/pending-%d", version, pending), func(t *testing.T) {
				scheme := runtime.NewScheme()
				if err := corev1.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				workloadAPI, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, version)
				if err != nil {
					t.Fatal(err)
				}
				group := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gang", UID: "gang-uid"},
					Spec:       slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}}}}
				var pods []corev1.Pod
				objects := []client.Object{group}
				for i := range 2 + pending {
					pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("pod-%d", i), UID: types.UID(fmt.Sprintf("pod-%d-uid", i))},
						Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(group.Name)},
							SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}},
							Containers:      []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("32Mi")}}}}}}
					if i < 2 {
						pod.Status.Phase = corev1.PodSucceeded
						if i == 1 {
							pod.Status.Phase = corev1.PodFailed
						}
						pod.Spec.NodeName = "old-node"
						pod.Spec.SchedulingGates = nil
						pod.Labels = map[string]string{wellknown.LabelExternalJobId: "10"}
						// Historical resource requests must not inflate the next allocation.
						pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("8")
						pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("1Gi")
					}
					pods = append(pods, pod)
					objects = append(objects, pod.DeepCopy())
				}
				kube := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
				root := metav1.PartialObjectMetadata{TypeMeta: group.TypeMeta, ObjectMeta: group.ObjectMeta}
				oldInfo := schedulercontrol.ExternalJobInfoFor(root, slurmjobir.SlurmJobComponent{Pods: corev1.PodList{Items: pods[:2]}})
				base := slurmfake.NewClientBuilder().WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(10)), AdminComment: ptr.To(oldInfo.ToString()), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCOMPLETED}}).Build()
				submissions := 0
				slurm := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, obj object.Object, request any, opts ...slurmclient.CreateOption) error {
					submissions++
					desc := request.(api.V0044JobSubmitReq).Job
					if desc == nil || ptr.Deref(desc.Nodes, "") != "2" || ptr.Deref(desc.MinimumNodes, 0) != 2 || ptr.Deref(desc.MaximumNodes, 0) != 2 {
						t.Fatalf("next batch must request two nodes: %+v", desc)
					}
					if ptr.Deref(desc.CpusPerTask, 0) != 1 || desc.MemoryPerNode == nil || ptr.Deref(desc.MemoryPerNode.Number, 0) != 32 {
						t.Fatalf("historical resource requests leaked into submission: %+v", desc)
					}
					var info externaljobinfo.ExternalJobInfo
					if err := externaljobinfo.ParseIntoExternalJobInfo(desc.AdminComment, &info); err != nil {
						t.Fatal(err)
					}
					if len(info.Pods) != 2 {
						t.Fatalf("submitted members: %v", info.Pods)
					}
					for _, key := range info.Pods {
						if key != externaljobinfo.PodKey(&pods[2]) && key != externaljobinfo.PodKey(&pods[3]) {
							t.Fatalf("submitted historical member %s", key)
						}
					}
					job := obj.(*api.V0044JobInfo)
					job.JobId, job.AdminComment = ptr.To(int32(20)), desc.AdminComment
					job.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}
					return base.Create(ctx, obj, nil)
				}})
				r := NewReconciler(kube, slurm, "slurm-bridge", dra.DefaultRegistry(), workloadAPI, "", "")
				r.informer = &registrationInformer{InformerCache: base.GetInformer(api.ObjectTypeV0044JobInfo), synced: true}
				if _, err := slurmjob.NewPodIndex(slurm, r.informer); err != nil {
					t.Fatal(err)
				}
				key := objectRequest(group)
				members, _, err := r.members(t.Context(), key)
				if err != nil || len(members) != len(pods) {
					t.Fatalf("cleanup lost historical membership: members=%d err=%v", len(members), err)
				}
				for range 2 {
					if _, err := r.Reconcile(t.Context(), key); err != nil {
						t.Fatal(err)
					}
				}
				wantSubmissions := pending / 2
				if submissions != wantSubmissions {
					t.Fatalf("submissions=%d, want %d", submissions, wantSubmissions)
				}
				for i := range pods {
					actual := &corev1.Pod{}
					if err := kube.Get(t.Context(), client.ObjectKeyFromObject(&pods[i]), actual); err != nil {
						t.Fatal(err)
					}
					wantID := ""
					if i < 2 {
						wantID = "10"
					} else if pending == 2 {
						wantID = "20"
					}
					if actual.Labels[wellknown.LabelExternalJobId] != wantID {
						t.Fatalf("%s: job label=%q, want %q", actual.Name, actual.Labels[wellknown.LabelExternalJobId], wantID)
					}
					if i >= 2 && hasRegistrationGate(actual) != (pending < 2) {
						t.Fatalf("%s: unexpected registration gate", actual.Name)
					}
				}
			})
		}
	}
}
