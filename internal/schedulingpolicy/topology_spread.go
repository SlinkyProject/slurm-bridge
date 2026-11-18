// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateTopologySpread(pod *corev1.Pod) field.ErrorList {
	if len(pod.Spec.TopologySpreadConstraints) == 0 {
		return nil
	}
	return field.ErrorList{field.Forbidden(field.NewPath("spec", "topologySpreadConstraints"), "topology spreading is unsupported: hard constraints require allocation-wide topology accounting and soft constraints require Kubernetes scoring")}
}
