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

	slurmJobIR := &SlurmJobIR{
		Components: []SlurmJobComponent{
			*fromJobSpec([]corev1.Pod{*pod}, &job.Spec),
		},
	}

	return slurmJobIR, nil
}

func fromJobSpec(pods []corev1.Pod, jobSpec *batchv1.JobSpec) *SlurmJobComponent {
	slurmJobComponent := new(SlurmJobComponent)

	slurmJobComponent.JobInfo.MinNodes = ptr.To(int32(len(pods))) //nolint:gosec // Pod count is bounded by Kubernetes object limits.

	slurmJobComponent.Pods.Items = append(slurmJobComponent.Pods.Items, pods...)
	// Only map a strictly positive deadline: a Slurm TimeLimit of 0 means
	// unlimited (forever), the opposite of a zero K8s deadline, so leave it
	// unset and let Slurm apply the partition default instead.
	if jobSpec.ActiveDeadlineSeconds != nil && *jobSpec.ActiveDeadlineSeconds > 0 {
		// K8s deadline is seconds; Slurm TimeLimit is minutes. Round up so the
		// job isn't cut short below its requested deadline.
		slurmJobComponent.JobInfo.TimeLimit = ptr.To(int32((*jobSpec.ActiveDeadlineSeconds + 59) / 60)) //nolint:gosec // disable G115
	}
	if jobSpec.Template.Spec.Resources != nil {
		slurmJobComponent.JobInfo.CpuPerTask = ptr.To(int32(jobSpec.Template.Spec.Resources.Limits.Cpu().Value())) //nolint:gosec // disable G115
		slurmJobComponent.JobInfo.MemPerNode = ptr.To(int64(GetMemoryFromQuantity(jobSpec.Template.Spec.Resources.Limits.Memory())))
	}

	return slurmJobComponent
}
