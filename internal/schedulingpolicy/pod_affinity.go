// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validatePodAffinity(pod *corev1.Pod) field.ErrorList {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAffinity == nil {
		return nil
	}
	affinity := pod.Spec.Affinity.PodAffinity
	path := field.NewPath("spec", "affinity", "podAffinity")
	var errs field.ErrorList
	if len(affinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
		errs = append(errs, field.Forbidden(path.Child("requiredDuringSchedulingIgnoredDuringExecution"), "inter-pod affinity requires allocation-wide placement and is unsupported"))
	}
	if len(affinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
		errs = append(errs, field.Forbidden(path.Child("preferredDuringSchedulingIgnoredDuringExecution"), "preferred inter-pod affinity is unsupported because Slurm controls scoring"))
	}
	return errs
}
