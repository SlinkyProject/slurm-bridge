// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	// Ref: https://kubernetes.io/docs/concepts/workloads/pods/
	pod_v1 = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
)

func (t *translator) fromPod(pod *corev1.Pod) (*SlurmJobIR, error) {
	slurmJobIR := new(SlurmJobIR)
	slurmJobComponent := new(SlurmJobComponent)

	slurmJobComponent.Pods.Items = append(slurmJobComponent.Pods.Items, *pod)
	tasks := int32(1)
	slurmJobComponent.JobInfo.TasksPerNode = &tasks
	slurmJobComponent.JobInfo.MaxNodes = &tasks

	slurmJobIR.Components = []SlurmJobComponent{
		*slurmJobComponent,
	}
	return slurmJobIR, nil
}
