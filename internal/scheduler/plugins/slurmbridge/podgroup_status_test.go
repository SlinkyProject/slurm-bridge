// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func Test_podsHaveSlurmNodeAssignments(t *testing.T) {
	t.Parallel()
	pods := &corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{wellknown.LabelExternalJobId: "1"},
					Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node-a"},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
				},
			},
		},
	}
	if podsHaveSlurmNodeAssignments(pods, "1") {
		t.Fatal("expected false when a pod lacks node annotation")
	}
	pods.Items[1].Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node-b"}
	if !podsHaveSlurmNodeAssignments(pods, "1") {
		t.Fatal("expected true when all pods have job id and node")
	}
	if podsHaveSlurmNodeAssignments(pods, "2") {
		t.Fatal("expected false when job id does not match")
	}
}
