// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestSchedulingRecordedMembers(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial-%t", partial), func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			workloadAPI, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
			if err != nil {
				t.Fatal(err)
			}
			group := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gang", UID: "gang-uid"}, Spec: slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1.PodGroupSchedulingPolicy{Gang: &schedulingv1.GangSchedulingPolicy{MinCount: 2}}}}
			objects := []client.Object{group}
			var pods []*corev1.Pod
			for _, name := range []string{"first", "second", "replacement"} {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name), Labels: map[string]string{wellknown.LabelExternalJobId: "10"}}, Spec: corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("gang")}, Containers: []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}}}}}
				if name == "replacement" || partial && name == "second" {
					pod.Labels = nil
					pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}
				}
				if name == "replacement" {
					pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("64")
				}
				objects = append(objects, pod)
				pods = append(pods, pod)
			}
			info := externaljobinfo.ExternalJobInfo{PodGroup: externaljobinfo.NewGroupIdentity(metav1.PartialObjectMetadata{TypeMeta: group.TypeMeta, ObjectMeta: group.ObjectMeta}), Pods: []string{externaljobinfo.PodKey(pods[0]), externaljobinfo.PodKey(pods[1])}}
			slurm := slurmfake.NewFakeClient(&api.V0044JobInfo{JobId: ptr.To(int32(10)), AdminComment: ptr.To(info.ToString()), JobState: ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING})})
			control, err := slurmcontrol.NewControl(slurm, slurm.GetInformer(api.ObjectTypeV0044JobInfo), "", "")
			if err != nil {
				t.Fatal(err)
			}
			kube := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			sb := &SlurmBridge{Client: kube, workloadAPI: workloadAPI, slurmControl: control}
			for _, native := range []bool{false, true} {
				cycle := framework.NewCycleState()
				if native {
					cycle.SetPodGroupSchedulingCycle(framework.NewCycleState())
				}
				_, status := sb.PreFilter(t.Context(), cycle, pods[0], nil)
				if status.IsSuccess() == partial {
					t.Fatalf("native=%t partial=%t status=%v", native, partial, status)
				}
				if partial {
					if status.Message() != "waiting for Slurm registration metadata" {
						t.Fatalf("partial publication must wait: %v", status)
					}
					continue
				}
				data, err := cycle.Read(stateKey)
				if err != nil {
					t.Fatal(err)
				}
				ir := data.(*stateData).slurmJobIR
				if len(ir.AllPods()) != 2 || ptr.Deref(ir.Components[0].JobInfo.CpuPerTask, 0) != 1 || ptr.Deref(ir.Components[0].JobInfo.MinNodes, 0) != 2 || ptr.Deref(ir.Components[0].JobInfo.MaxNodes, 0) != 2 {
					t.Fatalf("waiting member changed allocation: %+v", ir.Components[0])
				}
			}
		})
	}
}
