// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestPostFilterCannotReleaseAfterGroupReadinessFailure(t *testing.T) {
	scheme, err := newClientScheme()
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "default", Labels: map[string]string{sched.PodGroupLabel: "group", wellknown.LabelExternalJobId: "1"}}}
	group := &sched.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: "group", Namespace: "default"}, Spec: sched.PodGroupSpec{MinMember: 2}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, group).Build()
	slurm := &postFilterSlurmControl{getJobs: []*slurmcontrol.ExternalJob{{JobId: 1, Pending: true, Held: true}}, updateJob: func(*slurmjobir.SlurmJobIR) (int32, error) {
		t.Fatal("PostFilter updated an allocation after group readiness failed")
		return 0, nil
	}}
	sb := &SlurmBridge{Client: kubeClient, slurmControl: slurm}
	state := framework.NewCycleState()
	_, before := sb.PreFilter(context.Background(), state, pod, nil)
	if before.IsSuccess() || before.Message() != slurmjobir.ErrorExternalJobInvalid.Error() {
		t.Fatalf("expected incomplete group: %v", before)
	}
	_, after := sb.PostFilter(context.Background(), state, pod, nil)
	if after.Code() != before.Code() || after.Message() != before.Message() {
		t.Fatalf("readiness status changed: %v to %v", before, after)
	}
	if slurm.getJobCalls != 0 || slurm.releaseCalls != 0 || slurm.deleteCalls != 1 {
		t.Fatalf("PostFilter attempted allocation work: %v", slurm)
	}
}
