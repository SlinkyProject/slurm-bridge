// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"reflect"
	"slices"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestRegistrationAdmission(t *testing.T) {
	r := &PodAdmission{SchedulerName: "slurm-bridge"}
	ctx := admission.NewContextWithRequest(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Create}})
	pod := &corev1.Pod{Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", SchedulingGates: []corev1.PodSchedulingGate{{Name: "other/gate"}}, Tolerations: []corev1.Toleration{{Key: "other", Operator: corev1.TolerationOpExists}}}}
	if err := r.Default(ctx, pod); err != nil {
		t.Fatal(err)
	}
	want := pod.DeepCopy()
	if err := r.Default(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pod, want) {
		t.Fatal("admission reinvocation duplicated metadata")
	}
	if len(pod.Spec.SchedulingGates) != 2 || !slices.Contains(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: wellknown.SchedulingGateRegistration}) {
		t.Fatalf("gates=%v", pod.Spec.SchedulingGates)
	}
	if !slices.Contains(pod.Spec.Tolerations, *utils.NewTolerationNodeBridged("slurm-bridge")) {
		t.Fatal("missing node toleration")
	}
	pod.Spec.SchedulingGates = pod.Spec.SchedulingGates[:1]
	update := admission.NewContextWithRequest(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update}})
	if err := r.Default(update, pod); err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.SchedulingGates) != 1 {
		t.Fatal("update added an immutable scheduling gate")
	}
	foreign := &corev1.Pod{Spec: corev1.PodSpec{SchedulerName: "other"}}
	if err := r.Default(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if len(foreign.Spec.SchedulingGates) != 0 || len(foreign.Spec.Tolerations) != 0 {
		t.Fatal("changed a foreign scheduler's Pod")
	}
}
