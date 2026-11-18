// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateComponent requires a common hard node policy within a shared Slurm
// component. Distinct heterogeneous components may have distinct policies.
func ValidateComponent(pods []corev1.Pod) error {
	if len(pods) < 2 {
		return nil
	}
	first := &pods[0]
	for i := 1; i < len(pods); i++ {
		pod := &pods[i]
		var errs field.ErrorList
		detail := "shared allocation members must use the same hard node policy; member " + first.Namespace + "/" + first.Name + " differs; use separate allocations"
		if !maps.Equal(first.Spec.NodeSelector, pod.Spec.NodeSelector) {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "nodeSelector"), detail))
		}
		if !apiequality.Semantic.DeepEqual(requiredNodeAffinity(first), requiredNodeAffinity(pod)) {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution"), detail))
		}
		if err := PodError(pod, errs); err != nil {
			return err
		}
	}
	return nil
}

func requiredNodeAffinity(pod *corev1.Pod) *corev1.NodeSelector {
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
		return nil
	}
	return pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
}
