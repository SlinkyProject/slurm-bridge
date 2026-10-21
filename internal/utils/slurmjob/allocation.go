// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

var ErrAmbiguousAllocation = errors.New("multiple active Slurm allocations")
var ErrAllocationMissing = errors.New("waiting for Slurm registration")

type RegisteredJob struct {
	ExternalJob
	info externaljobinfo.ExternalJobInfo
}

// IndexedAllocation reads the allocation from the informer, without a live list.
// Always include legacy PodGroup candidates, even when the group index matches:
// otherwise an allocation in the newer format could hide an older duplicate.
func IndexedAllocation(ctx context.Context, informer slurmclient.InformerCache, identity AllocationIdentity) ([]RegisteredJob, error) {
	// Scheduler updates can add group metadata to an old allocation. Read the
	// legacy index first so that transition cannot make us miss both formats.
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
		jobs = append(jobs, RegisteredJob{ExternalJob: job, info: info})
	}
	if conflict {
		return jobs, fmt.Errorf("%w for %s", ErrAmbiguousAllocation, identity.Key)
	}
	return jobs, nil
}

func MatchAllocationComponents(ir *slurmjobir.SlurmJobIR, jobs []RegisteredJob) ([]ExternalJob, bool, error) {
	// A heterogeneous allocation remains active while any component is active.
	// Keep its finished components for identity matching, but never fill a gap
	// using history from a different allocation.
	var allocationID int32
	for _, job := range jobs {
		if job.Finished {
			continue
		}
		if allocationID != 0 && allocationID != job.AllocationID() {
			return nil, false, ErrAmbiguousAllocation
		}
		allocationID = job.AllocationID()
	}
	if allocationID == 0 {
		return nil, false, nil
	}
	components := make([]ExternalJob, len(ir.Components))
	for i, component := range ir.Components {
		for _, job := range jobs {
			if job.AllocationID() != allocationID {
				continue
			}
			if ir.RootPOM.Kind == "CompositePodGroup" && !ptr.Equal(job.info.PodGroup, externaljobinfo.NewGroupIdentity(component.ObjectMeta)) {
				continue
			}
			if ir.RootPOM.Kind == "PodGroup" && job.info.PodGroup == nil {
				// Old allocations have no group UID to establish membership.
				// Adopt only when their recorded Pod identities cover the group.
				// A partial match must not label new or replacement members.
				for _, member := range component.Pods.Items {
					if !slices.Contains(job.info.Pods, externaljobinfo.PodKey(&member)) &&
						!slices.Contains(job.info.Pods, member.Namespace+"/"+member.Name) {
						return nil, false, nil
					}
				}
			}
			if components[i].JobId != 0 {
				return nil, false, fmt.Errorf("%w for a registration component", ErrAmbiguousAllocation)
			}
			components[i] = job.ExternalJob
		}
		if components[i].JobId == 0 {
			return nil, false, nil
		}
	}
	return components, true, nil
}
