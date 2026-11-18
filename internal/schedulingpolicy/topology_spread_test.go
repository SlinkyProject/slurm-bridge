// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestTopologySpread(t *testing.T) {
	for _, action := range []corev1.UnsatisfiableConstraintAction{corev1.DoNotSchedule, corev1.ScheduleAnyway} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{WhenUnsatisfiable: action, TopologyKey: "zone", MaxSkew: 1}}}}
		if errs := validateTopologySpread(pod); len(errs) != 1 || errs[0].Field != "spec.topologySpreadConstraints" {
			t.Fatal(errs)
		}
	}
	if errs := validateTopologySpread(&corev1.Pod{}); len(errs) != 0 {
		t.Fatal(errs)
	}
}
