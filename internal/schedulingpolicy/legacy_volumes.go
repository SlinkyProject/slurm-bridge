// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func validateVolumeSources(pod *corev1.Pod) field.ErrorList {
	var errs field.ErrorList
	for i, volume := range pod.Spec.Volumes {
		source := volume.VolumeSource
		// These sources are kubelet-managed or have dedicated capability checks.
		// Clearing only known fields makes new API sources default to rejection.
		source.EmptyDir = nil
		source.ConfigMap = nil
		source.Secret = nil
		source.Projected = nil
		source.DownwardAPI = nil
		source.NFS = nil
		source.Image = nil
		source.HostPath = nil
		source.PersistentVolumeClaim = nil
		source.CSI = nil
		source.Ephemeral = nil
		if source != (corev1.VolumeSource{}) {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "volumes").Index(i), "this volume source is unsupported; supported kubelet sources are emptyDir, configMap, secret, projected, downwardAPI, image and NFS"))
		}
	}
	return errs
}
