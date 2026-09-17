// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func Test_translator_fromPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "workload",
		Name:      "pod-a",
	}}
	want := &SlurmJobIR{Components: []SlurmJobComponent{{
		JobInfo: SlurmJobIRJobInfo{
			MaxNodes:     ptr.To(int32(1)),
			TasksPerNode: ptr.To(int32(1)),
		},
		Pods: corev1.PodList{Items: []corev1.Pod{*pod}},
	}}}

	got, err := (&translator{}).fromPod(pod)
	if err != nil {
		t.Fatalf("translator.fromPod() error = %v, want nil", err)
	}
	if !apiequality.Semantic.DeepEqual(got, want) {
		t.Errorf("translator.fromPod() = %v, want %v", got, want)
	}
}
