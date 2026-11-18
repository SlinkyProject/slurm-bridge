// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPodAffinity(t *testing.T) {
	for _, a := range []*corev1.PodAffinity{nil, {}, {RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{TopologyKey: "zone"}}}, {PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{Weight: 1}}}} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{PodAffinity: a}}}
		errs := validatePodAffinity(pod)
		want := a != nil && (len(a.RequiredDuringSchedulingIgnoredDuringExecution) > 0 || len(a.PreferredDuringSchedulingIgnoredDuringExecution) > 0)
		if (len(errs) > 0) != want {
			t.Fatalf("affinity %v: errors %v", a, errs)
		}
		if want && !strings.HasPrefix(errs[0].Field, "spec.affinity.podAffinity.") {
			t.Fatal(errs)
		}
	}
}
