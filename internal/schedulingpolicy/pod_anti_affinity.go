// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validatePodAntiAffinity(pod *corev1.Pod) field.ErrorList {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
		return nil
	}
	affinity := pod.Spec.Affinity.PodAntiAffinity
	path := field.NewPath("spec", "affinity", "podAntiAffinity")
	var errs field.ErrorList
	if len(affinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
		errs = append(errs, field.Forbidden(path.Child("requiredDuringSchedulingIgnoredDuringExecution"), "inter-pod anti-affinity requires allocation-wide placement and is unsupported"))
	}
	if len(affinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
		errs = append(errs, field.Forbidden(path.Child("preferredDuringSchedulingIgnoredDuringExecution"), "preferred inter-pod anti-affinity is unsupported because Slurm controls scoring"))
	}
	return errs
}
