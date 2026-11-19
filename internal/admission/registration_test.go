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
	"k8s.io/utils/ptr"
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
	native := &corev1.Pod{Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}}}
	if err := r.Default(ctx, native); err != nil {
		t.Fatal(err)
	}
	if len(native.Spec.SchedulingGates) != 0 {
		t.Fatalf("gated a native PodGroup member: %v", native.Spec.SchedulingGates)
	}
	assigned := &corev1.Pod{Spec: corev1.PodSpec{SchedulerName: r.SchedulerName, NodeName: "worker"}}
	if err := r.Default(ctx, assigned); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(assigned.Spec.SchedulingGates, []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}) || assigned.Spec.NodeName != "" {
		t.Fatalf("preassigned pod after defaulting: %+v", assigned.Spec)
	}
	foreign := &corev1.Pod{Spec: corev1.PodSpec{SchedulerName: "other"}}
	if err := r.Default(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if len(foreign.Spec.SchedulingGates) != 0 || len(foreign.Spec.Tolerations) != 0 {
		t.Fatal("changed a foreign scheduler's Pod")
	}
}

func TestRegistrationGateValidation(t *testing.T) {
	r := &PodAdmission{SchedulerName: "slurm-bridge"}
	create := contextWithAdmissionOperation("CREATE")
	update := contextWithAdmissionOperation("UPDATE")
	for _, gate := range []string{"", "e2e.slurm.slinky.slurm.net/hold", "leaderworkerset.sigs.k8s.io/group-replacement"} {
		t.Run(gate, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{SchedulerName: r.SchedulerName}}
			if gate != "" {
				pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: gate}}
			}
			originalGates := slices.Clone(pod.Spec.SchedulingGates)
			if err := r.Default(create, pod); err != nil {
				t.Fatal(err)
			}
			if _, err := r.ValidateCreate(create, pod); err != nil {
				t.Fatalf("mutated bridge pod rejected: %v", err)
			}
			registered := pod.DeepCopy()
			registered.Spec.SchedulingGates = slices.DeleteFunc(registered.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool {
				return g.Name == wellknown.SchedulingGateRegistration
			})
			registered.Labels = map[string]string{wellknown.LabelExternalJobId: "100"}
			if !slices.Equal(registered.Spec.SchedulingGates, originalGates) {
				t.Fatalf("unrelated gates changed: %v", registered.Spec.SchedulingGates)
			}
			if _, err := r.ValidateUpdate(update, pod, registered); err != nil {
				t.Fatalf("controller publication rejected: %v", err)
			}
			ready := registered.DeepCopy()
			ready.Spec.SchedulingGates = nil
			if _, err := r.ValidateUpdate(update, registered, ready); err != nil {
				t.Fatalf("external gate removal rejected: %v", err)
			}
		})
	}
}
