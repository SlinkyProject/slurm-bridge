// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateHostLocalVolumes(pod *corev1.Pod) field.ErrorList {
	var errs field.ErrorList
	for i, volume := range pod.Spec.Volumes {
		if volume.HostPath != nil {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "volumes").Index(i).Child("hostPath"), "hostPath needs an explicit node-local data placement contract; use supported shared storage"))
		}
	}
	return errs
}
