// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateInlineCSIVolumes(pod *corev1.Pod) field.ErrorList {
	var errs field.ErrorList
	for i, volume := range pod.Spec.Volumes {
		if volume.CSI != nil {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "volumes").Index(i).Child("csi"), "inline CSI ephemeral volumes need a validated driver lifecycle and node availability contract; use supported kubelet volume sources"))
		}
	}
	return errs
}
