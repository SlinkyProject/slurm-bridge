// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	corev1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"

	"github.com/SlinkyProject/slurm-bridge/internal/schedulingpolicy"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

// Validate every member before the scheduler updates or releases a shared job.
func validateSchedulingPolicy(pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) *fwk.Status {
	if err := schedulingpolicy.PodError(pod, schedulingpolicy.ValidatePod(pod)); err != nil {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, err.Error())
	}
	if ir == nil {
		return nil
	}
	if err := schedulingpolicy.ValidateAllocation(ir); err != nil {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, err.Error())
	}
	return nil
}
