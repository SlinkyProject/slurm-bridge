// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func preferredPod() *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1}}}}}}
}

func TestPreferredNodeAffinity(t *testing.T) {
	for _, pod := range []*corev1.Pod{{}, {Spec: corev1.PodSpec{Affinity: &corev1.Affinity{}}}, {Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{}}}}} {
		if err := PodError(pod, ValidatePod(pod)); err != nil {
			t.Fatal(err)
		}
	}
	pod := preferredPod()
	err := PodError(pod, ValidatePod(pod))
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected Invalid, got %v", err)
	}
	var apiStatus apierrors.APIStatus
	if !errors.As(err, &apiStatus) {
		t.Fatalf("error has no API status: %v", err)
	}
	status := apiStatus.Status()
	if got := status.Details.Causes[0].Field; got != "spec.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution" {
		t.Fatalf("field = %q", got)
	}
	old := pod.DeepCopy()
	old.Spec.NodeName = "node1"
	updated := old.DeepCopy()
	updated.Labels = map[string]string{"bridge": "updated"}
	if err := PodError(updated, ValidateUpdate(old, updated)); err != nil {
		t.Fatalf("bound metadata update rejected: %v", err)
	}
	old.Spec.NodeName = ""
	if err := PodError(updated, ValidateUpdate(old, updated)); !apierrors.IsInvalid(err) {
		t.Fatalf("unbound pod escaped validation: %v", err)
	}
}
