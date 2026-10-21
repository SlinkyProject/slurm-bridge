// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	slurmutils "github.com/SlinkyProject/slurm-client/pkg/utils"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
)

// groupAllocations uses group identities for modern allocations. Legacy Pod-only
// records join a modern PodGroup only when their Pod identities identify one
// group. CompositePodGroups have no released legacy format.
func groupAllocations(jobs []api.V0044JobInfo) map[string]map[int32][]api.V0044JobInfo {
	groups := make(map[string]map[int32][]api.V0044JobInfo)
	owners := make(map[string]sets.Set[string])
	type legacyAllocation struct {
		job  api.V0044JobInfo
		id   int32
		pods []string
	}
	var legacy []legacyAllocation
	add := func(key string, id int32, job api.V0044JobInfo) {
		if groups[key] == nil {
			groups[key] = make(map[int32][]api.V0044JobInfo)
		}
		groups[key][id] = append(groups[key][id], job)
	}
	for _, raw := range jobs {
		job, err := slurmjob.FromJobInfo(&raw)
		if err != nil || job.Finished {
			continue
		}
		var info externaljobinfo.ExternalJobInfo
		if externaljobinfo.ParseIntoExternalJobInfo(raw.AdminComment, &info) != nil {
			continue
		}
		switch {
		case info.CompositePodGroup != nil:
			add(info.CompositePodGroup.Key(), job.AllocationID(), raw)
		case info.PodGroup != nil:
			key := info.PodGroup.Key()
			add(key, job.AllocationID(), raw)
			for _, podKey := range info.Pods {
				pod := externaljobinfo.ParsePodKey(podKey)
				for _, keyForPod := range []string{podKey, pod.Namespace + "/" + pod.Name} {
					if owners[keyForPod] == nil {
						owners[keyForPod] = sets.New[string]()
					}
					owners[keyForPod].Insert(key)
				}
			}
		default:
			legacy = append(legacy, legacyAllocation{job: raw, id: job.AllocationID(), pods: info.Pods})
		}
	}
	for _, old := range legacy {
		matches := sets.New[string]()
		for _, pod := range old.pods {
			matches = matches.Union(owners[pod])
		}
		if len(matches) == 1 {
			for key := range matches {
				add(key, old.id, old.job)
			}
			continue
		}
		// Do not use ambiguous legacy ownership to merge distinct modern groups.
		for _, pod := range old.pods {
			add("pod/"+pod, old.id, old.job)
		}
	}
	return groups
}

// Conflict discovery uses the allocation indexes. Only the exceptional recovery
// path lists the cached jobs to associate modern and pre-upgrade ownership.
func (r *Reconciler) cleanDuplicates(ctx context.Context, identity slurmjob.AllocationIdentity, pods []corev1.Pod) error {
	keys := sets.New(identity.Key)
	if identity.Index == slurmjob.PodJobIndex {
		keys = sets.New("pod/" + identity.Key)
	}
	for _, pod := range pods {
		keys.Insert("pod/"+externaljobinfo.PodKey(&pod), "pod/"+pod.Namespace+"/"+pod.Name)
	}
	return r.cleanDuplicateAllocations(ctx, keys)
}

func (r *Reconciler) cleanDuplicateAllocations(ctx context.Context, keys sets.Set[string]) error {
	jobs := &api.V0044JobInfoObjectList{}
	if err := r.slurmClient.List(ctx, jobs); err != nil {
		return err
	}
	var errs []error
	canceled := make(map[int32]bool)
	for key, allocations := range groupAllocations(jobs.Items) {
		if !keys.Has(key) || len(allocations) < 2 {
			continue
		}
		if err := r.cleanAllocationConflict(ctx, key, allocations, canceled); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) cleanAllocationConflict(ctx context.Context, key string, allocations map[int32][]api.V0044JobInfo, canceled map[int32]bool) error {
	var live []api.V0044JobInfo
	for _, components := range allocations {
		for _, cached := range components {
			job := &api.V0044JobInfo{}
			if err := r.slurmClient.Get(ctx, cached.GetKey(), job, &slurmclient.GetOptions{SkipCache: true}); err != nil {
				if errors.Is(err, slurmerrors.ErrNotFound) {
					continue
				}
				return err
			}
			if job.GetKey() != cached.GetKey() {
				return fmt.Errorf("slurm returned job %s for %s", job.GetKey(), cached.GetKey())
			}
			live = append(live, *job)
		}
	}
	// Rebuild ownership from the live records, including the legacy association.
	active := groupAllocations(live)[key]
	if len(active) < 2 {
		return nil
	}
	keep, err := survivingAllocation(active)
	if err != nil {
		return err
	}
	var errs []error
	for id := range active {
		if id == keep || canceled[id] {
			continue
		}
		if err := r.slurmClient.Delete(ctx, &api.V0044JobInfo{JobId: ptr.To(id)}); err != nil && !errors.Is(err, slurmerrors.ErrNotFound) {
			errs = append(errs, fmt.Errorf("cancel duplicate allocation %d: %w", id, err))
			continue
		}
		canceled[id] = true
	}
	return errors.Join(errs...)
}

// survivingAllocation picks the allocation to keep among duplicates.
func survivingAllocation(active map[int32][]api.V0044JobInfo) (int32, error) {
	var keep, released, lowest int32
	for id, components := range active {
		if lowest == 0 || id < lowest {
			lowest = id
		}
		for _, job := range components {
			current, err := slurmjob.FromJobInfo(&job)
			if err != nil {
				return 0, err
			}
			state := slurmutils.GetStateAsSet(job.JobState)
			// Preserve a started allocation, including a suspended or completing one.
			// If several have started, use the lowest leader ID consistently.
			if state.HasAny(api.V0044JobInfoJobStateRUNNING, api.V0044JobInfoJobStateSUSPENDED, api.V0044JobInfoJobStateCOMPLETING) && (keep == 0 || id < keep) {
				keep = id
			}
			// Preserve release progress, including a partially released hetjob,
			// before falling back to the oldest fully held allocation.
			if current.Pending && !current.Held && (released == 0 || id < released) {
				released = id
			}
		}
	}
	if keep == 0 {
		keep = released
	}
	if keep == 0 {
		keep = lowest
	}
	return keep, nil
}
