// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validatePreferredNodeAffinity(pod *corev1.Pod) field.ErrorList {
	affinity := pod.Spec.Affinity
	if affinity == nil || affinity.NodeAffinity == nil || len(affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) == 0 {
		return nil
	}
	return field.ErrorList{field.Forbidden(field.NewPath("spec", "affinity", "nodeAffinity", "preferredDuringSchedulingIgnoredDuringExecution"), "preferred node affinity is unsupported because Slurm controls node scoring; use a Slurm partition or constraint")}
}
