// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
)

var ErrAmbiguousAllocation = errors.New("multiple active Slurm allocations")
var ErrAllocationMissing = errors.New("waiting for Slurm registration")

type RegisteredJob struct {
	ExternalJob
	info      externaljobinfo.ExternalJobInfo
	hetJobIDs string
}

// Ownership returns the Kubernetes identities recorded by the allocation.
func (j RegisteredJob) Ownership() externaljobinfo.ExternalJobInfo { return j.info }

// RegisteredJobFromInfo decodes both allocation state and Kubernetes ownership.
func RegisteredJobFromInfo(raw *api.V0044JobInfo) (RegisteredJob, error) {
	job, err := FromJobInfo(raw)
	if err != nil {
		return RegisteredJob{}, err
	}
	info := externaljobinfo.ExternalJobInfo{}
	if err := externaljobinfo.ParseIntoExternalJobInfo(raw.AdminComment, &info); err != nil {
		return RegisteredJob{}, err
	}
	return RegisteredJob{ExternalJob: job, info: info, hetJobIDs: ptr.Deref(raw.HetJobIdSet, "")}, nil
}

// IndexedAllocation reads the allocation from the informer, without a live list.
// Always include legacy PodGroup candidates, even when the group index matches:
// otherwise an allocation in the newer format could hide an older duplicate.
func IndexedAllocation(ctx context.Context, informer slurmclient.InformerCache, identity AllocationIdentity) ([]RegisteredJob, error) {
	// Read the legacy index first: an older bridge may still add group
	// metadata during an update while this lookup is in progress.
	var legacyJobs []api.V0044JobInfo
	for _, key := range identity.legacyPodKeys {
		legacy := &api.V0044JobInfoObjectList{}
		if err := informer.ByIndex(ctx, LegacyPodJobIndex, key, legacy); err != nil {
			return nil, err
		}
		legacyJobs = append(legacyJobs, legacy.Items...)
	}
	cached := &api.V0044JobInfoObjectList{}
	if err := informer.ByIndex(ctx, identity.Index, identity.Key, cached); err != nil {
		return nil, err
	}
	if pod := externaljobinfo.ParsePodKey(identity.Key); identity.Index == PodJobIndex && len(cached.Items) == 0 && pod.UID != "" {
		if err := informer.ByIndex(ctx, PodJobIndex, pod.Namespace+"/"+pod.Name, cached); err != nil {
			return nil, err
		}
	}
	cached.Items = append(cached.Items, legacyJobs...)
	var jobs []RegisteredJob
	seen := make(map[int32]bool)
	var leader int32
	var conflict bool
	for _, candidate := range cached.Items {
		job, err := FromJobInfo(&candidate)
		if err != nil {
			return nil, err
		}
		if seen[job.JobId] {
			continue
		}
		seen[job.JobId] = true
		info := externaljobinfo.ExternalJobInfo{}
		if err := externaljobinfo.ParseIntoExternalJobInfo(candidate.AdminComment, &info); err != nil {
			return nil, err
		}
		base := job.AllocationID()
		if !job.Finished {
			if leader != 0 && leader != base {
				conflict = true
			}
			leader = base
		}
		jobs = append(jobs, RegisteredJob{ExternalJob: job, info: info, hetJobIDs: ptr.Deref(candidate.HetJobIdSet, "")})
	}
	if conflict {
		return jobs, fmt.Errorf("%w for %s", ErrAmbiguousAllocation, identity.Key)
	}
	return jobs, nil
}
