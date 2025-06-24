// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	job_v1 = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
)

// fromJobSet will translate a pod from a Job into a SlurmJobIR.
func (t *translator) fromJob(pod *corev1.Pod, rootPOM *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
	job := &batchv1.Job{}
	key := client.ObjectKey{Namespace: rootPOM.GetNamespace(), Name: rootPOM.Name}
	if err := t.Get(t.ctx, key, job); err != nil {
		return nil, err
	}

	slurmJobIR := &SlurmJobIR{}
	slurmJobIR.Pods.Items = append(slurmJobIR.Pods.Items, *pod)
	slurmJobIR.JobInfo.MinNodes = ptr.To(int32(1))
	if job.Spec.Template.Spec.Resources != nil {
		slurmJobIR.JobInfo.CpuPerTask = ptr.To(int32(job.Spec.Template.Spec.Resources.Limits.Cpu().Value())) //nolint:gosec // disable G115
		slurmJobIR.JobInfo.MemPerNode = ptr.To(int64(GetMemoryFromQuantity(job.Spec.Template.Spec.Resources.Limits.Memory())))
	}

	return slurmJobIR, nil
}
