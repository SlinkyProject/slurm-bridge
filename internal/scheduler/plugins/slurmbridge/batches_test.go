// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestSchedulerPodGroupNextBatch(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workloadAPI, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
	if err != nil {
		t.Fatal(err)
	}
	group := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta,
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gang", UID: "gang-uid"},
		Spec:       slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}}}}
	var pods []corev1.Pod
	objects := []client.Object{group}
	for i := range 4 {
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("pod-%d", i), UID: types.UID(fmt.Sprintf("pod-%d", i)), Labels: map[string]string{wellknown.LabelExternalJobId: "20"}},
			Spec: corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(group.Name)}}}
		if i < 2 {
			pod.Status.Phase = corev1.PodSucceeded
			if i == 1 {
				pod.Status.Phase = corev1.PodFailed
			}
			pod.Labels[wellknown.LabelExternalJobId] = "10"
			pod.Spec.NodeName = "old-node"
		}
		pods = append(pods, pod)
		objects = append(objects, pod.DeepCopy())
	}
	kube := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	ir, err := slurmjobir.TranslateToSlurmJobIR(kube, dra.DefaultRegistry(), workloadAPI, t.Context(), &pods[2])
	if err != nil {
		t.Fatal(err)
	}
	// The allocation was registered for just the new pair. Older terminal members
	// keep their old job ID and must neither block release nor expand this job.
	info := slurmcontrol.ExternalJobInfoFor(metav1.PartialObjectMetadata{TypeMeta: group.TypeMeta, ObjectMeta: group.ObjectMeta}, slurmjobir.SlurmJobComponent{Pods: corev1.PodList{Items: pods[2:]}})
	updates, releases := 0, 0
	slurm := fake.NewClientBuilder().WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(20)), AdminComment: ptr.To(info.ToString()), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}}).
		WithInterceptorFuncs(interceptor.Funcs{Update: func(_ context.Context, obj object.Object, request any, _ ...slurmclient.UpdateOption) error {
			if obj.GetKey() != "20" {
				t.Fatalf("updated historical job %s", obj.GetKey())
			}
			desc := request.(api.V0044JobDescMsg)
			if desc.Hold != nil {
				releases++
				return nil
			}
			updates++
			if ptr.Deref(desc.Nodes, "") != "2" || ptr.Deref(desc.MinimumNodes, 0) != 2 || ptr.Deref(desc.MaximumNodes, 0) != 2 {
				t.Fatalf("scheduler expanded the allocation: %+v", desc)
			}
			return nil
		}}).Build()
	sb := &SlurmBridge{Client: kube, slurmControl: newTestSlurmControl(t, slurm, "", "")}
	state := &stateData{slurmJobIR: ir}
	if status := sb.checkAllocation(t.Context(), state, &pods[2]); !status.IsSuccess() {
		t.Fatalf("historical labels blocked the next batch: %v", status)
	}
	if status := sb.updateExternalJob(t.Context(), state, true); !status.IsSuccess() {
		t.Fatal(status)
	}
	if updates != 1 || releases != 1 {
		t.Fatalf("updates=%d releases=%d; want one of each", updates, releases)
	}
}
