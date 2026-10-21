// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"errors"
	"fmt"

	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmutils "github.com/SlinkyProject/slurm-client/pkg/utils"
)

type ExternalJob struct {
	JobId        int32
	HetJobId     int32
	HetJobOffset int32
	Nodes        string
	Pending      bool
	Held         bool
	Finished     bool
}

func (j ExternalJob) AllocationID() int32 {
	if j.HetJobId > 0 {
		return j.HetJobId
	}
	return j.JobId
}

func FromJobInfo(job *api.V0044JobInfo) (ExternalJob, error) {
	if job == nil {
		return ExternalJob{}, errors.New("cannot convert nil Slurm job")
	}

	extJob := ExternalJob{
		JobId:    ptr.Deref(job.JobId, 0),
		Nodes:    ptr.Deref(job.Nodes, ""),
		Pending:  slurmutils.GetStateAsSet(job.JobState).Has(api.V0044JobInfoJobStatePENDING),
		Finished: isFinishedJob(job),
	}

	if job.Priority != nil {
		extJob.Held = extJob.Pending && ptr.Deref(job.Priority.Set, false) && ptr.Deref(job.Priority.Number, -1) == 0
	}
	return validateExternalJobID(job, extJob)
}

func validateExternalJobID(job *api.V0044JobInfo, extJob ExternalJob) (ExternalJob, error) {
	if extJob.JobId <= 0 {
		return ExternalJob{}, errors.New("invalid or unset job ID")
	}

	if job.HetJobId == nil || !ptr.Deref(job.HetJobId.Set, false) {
		return extJob, nil
	}
	if job.HetJobId.Number == nil {
		return ExternalJob{}, errors.New("malformed het-job: het job ID is set but its value is missing")
	}
	if job.HetJobOffset == nil {
		return ExternalJob{}, errors.New("malformed het-job: het job ID is set but het job offset is missing")
	}
	if !ptr.Deref(job.HetJobOffset.Set, false) {
		return ExternalJob{}, errors.New("malformed het-job: het job ID is set but het job offset is unset")
	}
	if job.HetJobOffset.Number == nil {
		return ExternalJob{}, errors.New("malformed het-job: het job offset is set but its value is missing")
	}

	extJob.HetJobId = *job.HetJobId.Number
	extJob.HetJobOffset = *job.HetJobOffset.Number

	if extJob.HetJobId == 0 {
		if extJob.HetJobOffset != 0 {
			return ExternalJob{}, errors.New("invalid het job offset: homogeneous job has non-zero offset")
		}
		return extJob, nil
	}
	if extJob.HetJobId < 0 {
		return ExternalJob{}, fmt.Errorf("invalid het job ID %d: value must be positive", extJob.HetJobId)
	}
	if extJob.HetJobOffset < 0 {
		return ExternalJob{}, errors.New("invalid het job offset: value is negative")
	}
	if extJob.HetJobId != extJob.JobId && extJob.HetJobOffset == 0 {
		return ExternalJob{}, errors.New("invalid het job offset: non-leader component has offset zero")
	}

	return extJob, nil
}

func isFinishedJob(job *api.V0044JobInfo) bool {
	state := slurmutils.GetStateAsSet(job.JobState)
	// Requeued jobs can retain PREEMPTED alongside their current active state.
	if state.HasAny(api.V0044JobInfoJobStatePENDING, api.V0044JobInfoJobStateRUNNING, api.V0044JobInfoJobStateSUSPENDED) {
		return false
	}
	return state.HasAny(
		api.V0044JobInfoJobStateCOMPLETED,
		api.V0044JobInfoJobStateCANCELLED,
		api.V0044JobInfoJobStateFAILED,
		api.V0044JobInfoJobStateTIMEOUT,
		api.V0044JobInfoJobStateNODEFAIL,
		api.V0044JobInfoJobStatePREEMPTED,
		api.V0044JobInfoJobStateBOOTFAIL,
		api.V0044JobInfoJobStateDEADLINE,
		api.V0044JobInfoJobStateOUTOFMEMORY,
	)
}
