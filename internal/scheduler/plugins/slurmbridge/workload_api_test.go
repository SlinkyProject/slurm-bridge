// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestPreFilterRejectsDisabledPodGroupsBeforeSideEffects(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "worker", Labels: map[string]string{wellknown.LabelExternalJobId: "17"}},
		Spec:       corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}},
	}
	// No clients are supplied: rejection must happen before Kubernetes or Slurm
	// requests, including validation and cleanup of existing external job IDs.
	sb := &SlurmBridge{}
	state := framework.NewCycleState()
	result, status := sb.PreFilter(context.Background(), state, pod, nil)
	if result != nil || status.Code() != fwk.UnschedulableAndUnresolvable {
		t.Fatalf("PreFilter() = %v, %v; want an unresolvable scheduling rejection", result, status)
	}
	if message := status.Message(); !strings.Contains(message, "GenericWorkload=true") || !strings.Contains(message, "default/worker") {
		t.Fatalf("rejection must explain how to enable support: %s", message)
	}
	if _, err := state.Read(stateKey); err == nil {
		t.Fatal("ineligible Pod should not have scheduling state")
	}
	if _, err := slurmjobir.TranslateToSlurmJobIR(nil, nil, nil, context.Background(), pod); err == nil {
		t.Fatal("direct translation must also reject disabled PodGroups")
	}
}

func TestValidatePodGroupSupport(t *testing.T) {
	for _, tt := range []struct {
		name    string
		group   *corev1.PodSchedulingGroup
		api     *slurmjobir.WorkloadAPI
		wantErr bool
	}{
		{name: "ordinary Pod remains eligible"},
		{name: "empty group has no reference", group: &corev1.PodSchedulingGroup{}},
		{name: "empty name has no reference", group: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("")}},
		{name: "disabled native group", group: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}, wantErr: true},
		{name: "enabled native group", group: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}, api: &slurmjobir.WorkloadAPI{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{SchedulingGroup: tt.group}}
			if err := slurmjobir.ValidatePodGroupSupport(tt.api, pod); (err != nil) != tt.wantErr {
				t.Fatalf("eligibility error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}
