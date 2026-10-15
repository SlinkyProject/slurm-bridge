// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestMetadataRepairIgnoresReplacedPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "work", UID: "new-uid",
		Labels: map[string]string{wellknown.LabelExternalJobId: "8"},
	}, Spec: corev1.PodSpec{SchedulerName: schedulerName}}
	kubeClient := kubefake.NewClientBuilder().WithObjects(pod).Build()
	slurmClient := fake.NewClientBuilder().WithObjects(&api.V0044JobInfo{
		JobId:        ptr.To(int32(7)),
		AdminComment: ptr.To(`{"pods":"default/work/old-uid"}`),
	}).Build()
	index, err := slurmjob.NewPodIndex(slurmClient, slurmClient.GetInformer(api.ObjectTypeV0044JobInfo))
	if err != nil {
		t.Fatal(err)
	}
	r := &PodReconciler{Client: kubeClient, jobIndex: index, SchedulerName: schedulerName}
	if _, err := r.reconcilePodMetadata(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(pod)}); err != nil {
		t.Fatal(err)
	}
	if got := pod.Labels[wellknown.LabelExternalJobId]; got != "8" {
		t.Errorf("in-memory job label = %q, want 8", got)
	}
	stored := &corev1.Pod{}
	if err := kubeClient.Get(t.Context(), client.ObjectKeyFromObject(pod), stored); err != nil {
		t.Fatal(err)
	}
	if got := stored.Labels[wellknown.LabelExternalJobId]; got != "8" {
		t.Errorf("stored job label = %q, want 8", got)
	}
}
