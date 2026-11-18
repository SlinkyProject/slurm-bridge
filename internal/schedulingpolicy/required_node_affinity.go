// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateRequiredNodeAffinity(pod *corev1.Pod) field.ErrorList {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil || pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return nil
	}
	return field.ErrorList{field.Forbidden(field.NewPath("spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution"), "required node affinity is blocked until NodeAffinity filtering is supported; use a Slurm partition or constraint")}
}
