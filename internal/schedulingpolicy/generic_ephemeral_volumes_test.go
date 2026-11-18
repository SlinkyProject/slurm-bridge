// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestGenericEphemeralVolumes(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Spec.Volumes = []corev1.Volume{{Name: "data"}}
	pod.Spec.Volumes[0].Ephemeral = &corev1.EphemeralVolumeSource{}
	if errs := validateGenericEphemeralVolumes(pod); len(errs) != 1 || errs[0].Field != "spec.volumes[0].ephemeral" {
		t.Fatal(errs)
	}
	pod.Spec.Volumes[0].Ephemeral = nil
	pod.Spec.Volumes[0].EmptyDir = &corev1.EmptyDirVolumeSource{}
	if errs := validateGenericEphemeralVolumes(pod); len(errs) != 0 {
		t.Fatal(errs)
	}
}
