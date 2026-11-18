// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBoundUpdatesPreserveOnlyUnchangedRequests(t *testing.T) {
	cases := []struct {
		name   string
		pod    *corev1.Pod
		change func(*corev1.Pod)
	}{
		{"preferred affinity", preferredPod(), func(p *corev1.Pod) {
			p.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].Weight = 2
		}},
		{"host path", &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/old"}}}}}}, func(p *corev1.Pod) { p.Spec.Volumes[0].HostPath.Path = "/new" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := tc.pod.DeepCopy()
			old.Spec.NodeName = "bound"
			old.Spec.Containers = []corev1.Container{{Name: "app", Image: "old"}}
			updated := old.DeepCopy()
			updated.Labels = map[string]string{"bridge": "updated"}
			updated.Spec.Containers[0].Image = "new"
			if errs := ValidateUpdate(old, updated); len(errs) > 0 {
				t.Fatalf("metadata/image update: %v", errs)
			}
			tc.change(updated)
			if errs := ValidateUpdate(old, updated); len(errs) == 0 {
				t.Fatal("changed unsupported request was grandfathered")
			}
		})
	}
	old := preferredPod()
	old.Spec.NodeName = "bound"
	updated := old.DeepCopy()
	updated.Spec.Affinity = nil
	if errs := ValidateUpdate(old, updated); len(errs) != 0 {
		t.Fatalf("removing a violation: %v", errs)
	}
}

func TestGeneratedResourceClaimsRemainValid(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "bridge-generated"}}}}
	if errs := ValidatePod(pod); len(errs) != 0 {
		t.Fatalf("generated claim rejected: %v", errs)
	}
}
