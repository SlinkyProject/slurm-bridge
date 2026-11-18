// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateGenericEphemeralVolumes(pod *corev1.Pod) field.ErrorList {
	var errs field.ErrorList
	for i, volume := range pod.Spec.Volumes {
		if volume.Ephemeral != nil {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "volumes").Index(i).Child("ephemeral"), "generic ephemeral volumes need validated PVC provisioning, ownership and cleanup; use supported kubelet volume sources"))
		}
	}
	return errs
}
