// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"slices"

	corev1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// translateAllocation resolves membership before interpreting resource requests.
// Both scheduler paths use the same selection and error handling.
func (sb *SlurmBridge) translateAllocation(ctx context.Context, pod *corev1.Pod) (slurmjob.Allocation, error) {
	var allocation slurmjob.Allocation
	ir, err := slurmjobir.TranslateToSlurmJobIR(sb.translationClient(), sb.registry(), sb.workloadAPI, ctx, pod,
		slurmjobir.WithResourceSlices(sb.resourceSlices),
		slurmjobir.WithNUMASpans(sb.numaCache),
		slurmjobir.WithAllocation(func(ir *slurmjobir.SlurmJobIR) (*slurmjobir.SlurmJobIR, error) {
			var err error
			allocation, err = sb.slurmControl.GetAllocation(ctx, pod, ir)
			return allocation.IR, err
		}))
	allocation.IR = ir
	return allocation, err
}

// preFilterAllocation confirms a native gang's quorum against its recorded members
// before an informer miss can cancel the Slurm job.
func (sb *SlurmBridge) preFilterAllocation(ctx context.Context, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) *fwk.Status {
	status := slurmjobir.PreFilter(sb.Client, sb.registry(), sb.handle, sb.workloadAPI, ctx, pod, ir)
	if status.IsSuccess() || !slices.Contains(status.Reasons(), slurmjobir.ErrorExternalJobInvalid.Error()) {
		return status
	}

	// Cleanup needs the full live workload. Quorum counts only the submitted batch.
	live, err := slurmjobir.TranslateToSlurmJobIR(sb.Client, sb.registry(), sb.workloadAPI, ctx, pod)
	if err != nil {
		return fwk.AsStatus(err)
	}
	if pod.Spec.SchedulingGroup != nil {
		allocation, err := sb.slurmControl.GetAllocation(ctx, pod, live)
		state := stateData{slurmJobIR: allocation.IR}
		if checked := state.checkAllocation(pod, allocation.Jobs, err); !checked.IsSuccess() {
			return checked
		}
		ready, _, err := slurmjobir.RegistrationReady(sb.Client, sb.registry(), sb.workloadAPI, ctx, pod, allocation.IR)
		if err != nil {
			return fwk.AsStatus(err)
		}
		if ready {
			return fwk.NewStatus(fwk.Unschedulable, "waiting for PodGroup membership to reach the scheduler")
		}
	}
	if err := sb.deleteTranslatedJob(ctx, pod, live); err != nil {
		return fwk.AsStatus(err)
	}
	return status
}

func allocationWait(err error) bool {
	return errors.Is(err, slurmjob.ErrAmbiguousAllocation) || errors.Is(err, slurmjob.ErrAllocationMissing) || errors.Is(err, slurmjob.ErrAllocationMembership)
}

// Pin all component IDs for the cycle. A refreshed index can invalidate this
// selection, but cannot switch a scheduling attempt to another allocation.
func (sb *SlurmBridge) checkAllocation(ctx context.Context, s *stateData, pod *corev1.Pod) *fwk.Status {
	allocation, err := sb.slurmControl.GetAllocation(ctx, pod, s.slurmJobIR)
	return s.checkAllocation(pod, allocation.Jobs, err)
}

// Confirm a cached placement before a scheduling failure can cancel it.
func (sb *SlurmBridge) confirmPlacement(ctx context.Context, pod *corev1.Pod, expected slurmcontrol.ExternalJob) *fwk.Status {
	job, err := sb.slurmControl.GetJob(ctx, pod)
	if err != nil {
		return fwk.AsStatus(err)
	}
	if job.JobId != expected.JobId || job.HetJobId != expected.HetJobId ||
		job.Nodes != expected.Nodes || job.Pending || job.Finished {
		return fwk.NewStatus(fwk.Unschedulable, "allocation changed before placement rejection was confirmed")
	}
	return nil
}

func (s *stateData) checkAllocation(pod *corev1.Pod, jobs []slurmcontrol.ExternalJob, err error) *fwk.Status {
	if err != nil {
		if allocationWait(err) {
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
