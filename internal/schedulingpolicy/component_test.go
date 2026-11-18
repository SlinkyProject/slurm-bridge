// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestComponentNodePolicies(t *testing.T) {
	first := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader"}, Spec: corev1.PodSpec{NodeSelector: map[string]string{"rack": "a"}}}
	second := *first.DeepCopy()
	second.Name = "worker"
	if err := ValidateComponent([]corev1.Pod{first, second}); err != nil {
		t.Fatal(err)
	}
	second.Spec.NodeSelector["rack"] = "b"
	if err := ValidateComponent([]corev1.Pod{first, second}); err == nil || !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "leader") {
		t.Fatalf("different policies: %v", err)
	}
	first.Spec.NodeSelector = nil
	second.Spec.NodeSelector = map[string]string{}
	if err := ValidateComponent([]corev1.Pod{first, second}); err != nil {
		t.Fatalf("nil and empty selectors: %v", err)
	}
	if err := ValidateComponent([]corev1.Pod{first}); err != nil {
		t.Fatalf("individual allocation: %v", err)
	}
}
