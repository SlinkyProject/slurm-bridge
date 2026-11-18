// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateCreate also checks direct node targeting, which would bypass Slurm.
// Binding and subsequent updates naturally set nodeName and must stay allowed.
func ValidateCreate(pod *corev1.Pod) field.ErrorList {
	return append(ValidatePod(pod), ValidateNodeName(pod)...)
}

// ValidateNodeName is used before admission could erase a user's node target.
func ValidateNodeName(pod *corev1.Pod) field.ErrorList {
	if pod.Spec.NodeName == "" {
		return nil
	}
	return field.ErrorList{field.Forbidden(field.NewPath("spec", "nodeName"), "direct node targeting bypasses Slurm scheduling; use a Slurm partition or constraint")}
}
