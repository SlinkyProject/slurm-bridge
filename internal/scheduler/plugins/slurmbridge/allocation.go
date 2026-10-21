// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Pin all component IDs for the cycle. A refreshed index can invalidate this
// selection, but cannot switch a scheduling attempt to another allocation.
func (sb *SlurmBridge) checkAllocation(ctx context.Context, s *stateData, pod *corev1.Pod) *fwk.Status {
	jobs, err := sb.slurmControl.GetAllocation(ctx, pod, s.slurmJobIR)
	return s.checkAllocation(pod, jobs, err)
}

func (s *stateData) checkAllocation(pod *corev1.Pod, jobs []slurmcontrol.ExternalJob, err error) *fwk.Status {
	if err != nil {
		if errors.Is(err, slurmjob.ErrAmbiguousAllocation) || errors.Is(err, slurmjob.ErrAllocationMissing) {
			return fwk.NewStatus(fwk.Unschedulable, err.Error())
		}
		return fwk.AsStatus(err)
	}
	if len(jobs) == 0 || len(jobs) != len(s.slurmJobIR.Components) {
		return fwk.NewStatus(fwk.Unschedulable, "waiting for Slurm registration")
	}
	for i, job := range jobs {
		if len(s.allocation) > 0 && (s.allocation[i].JobId != job.JobId || s.allocation[i].HetJobId != job.HetJobId) {
			return fwk.NewStatus(fwk.Unschedulable, "Slurm allocation changed during scheduling")
		}
		if job.Finished {
			continue
		}
		for _, member := range s.slurmJobIR.Components[i].Pods.Items {
			if slurmjobir.ParseSlurmJobId(member.Labels[wellknown.LabelExternalJobId]) != job.JobId {
				return fwk.NewStatus(fwk.Unschedulable, "waiting for Slurm registration metadata")
			}
		}
	}
	component := s.slurmJobIR.ComponentOf(pod.Namespace, pod.Name)
	if component < 0 || slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId]) != jobs[component].JobId {
		return fwk.NewStatus(fwk.Unschedulable, "waiting for Slurm registration metadata")
	}
	if jobs[component].Finished {
		return fwk.NewStatus(fwk.Unschedulable, "Slurm component has finished")
	}
	if len(s.allocation) == 0 {
		s.allocation = jobs
	}
	return nil
}
