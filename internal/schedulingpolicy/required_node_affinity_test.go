// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestRequiredNodeAffinity(t *testing.T) {
	// Even an empty required selector has semantics (it matches no nodes), so
	// absence of selector terms must not bypass the temporary rejection.
	for _, selector := range []*corev1.NodeSelector{{}, {NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node1"}}}}}}} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: selector}}}}
		if errs := validateRequiredNodeAffinity(pod); len(errs) != 1 || errs[0].Field != "spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution" {
			t.Fatal(errs)
		}
	}
	if errs := validateRequiredNodeAffinity(&corev1.Pod{}); len(errs) != 0 {
		t.Fatal(errs)
	}
}
