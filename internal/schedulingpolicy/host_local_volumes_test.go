// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestHostLocalVolumes(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Spec.Volumes = []corev1.Volume{{Name: "data"}}
	pod.Spec.Volumes[0].HostPath = &corev1.HostPathVolumeSource{Path: "/data"}
	if errs := validateHostLocalVolumes(pod); len(errs) != 1 || errs[0].Field != "spec.volumes[0].hostPath" {
		t.Fatal(errs)
	}
	pod.Spec.Volumes[0].HostPath = nil
	pod.Spec.Volumes[0].EmptyDir = &corev1.EmptyDirVolumeSource{}
	if errs := validateHostLocalVolumes(pod); len(errs) != 0 {
		t.Fatal(errs)
	}
}
