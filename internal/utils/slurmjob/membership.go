// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/SlinkyProject/slurm-client/pkg/hostlist"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

var ErrAllocationMembership = errors.New("pod is not registered for this Slurm allocation")

// Allocation pairs the submitted batch's current Pods with their component jobs.
type Allocation struct {
	IR   *slurmjobir.SlurmJobIR
	Jobs []ExternalJob
}

type AllocationPodIndex map[string]sets.Set[int32]

func IndexAllocationPods(jobs []RegisteredJob) AllocationPodIndex {
	index := make(AllocationPodIndex)
	for _, job := range jobs {
		for _, key := range job.info.Pods {
			if index[key] == nil {
				index[key] = sets.New[int32]()
			}
			index[key].Insert(job.JobId)
		}
	}
	return index
}

func (index AllocationPodIndex) Contains(pod *corev1.Pod, jobID int32) bool {
	return index[externaljobinfo.PodKey(pod)].Has(jobID) || index[pod.Namespace+"/"+pod.Name].Has(jobID)
}

// SelectAllocation intersects current membership with one submitted allocation.
// Finished or emptied leaves do not prevent surviving members from progressing;
// quorum is checked separately against the selected batch.
func SelectAllocation(ir *slurmjobir.SlurmJobIR, jobs []RegisteredJob) (Allocation, error) {
	var leader int32
	for _, job := range jobs {
		if job.Finished {
			continue
		}
		if leader != 0 && leader != job.AllocationID() {
			return Allocation{}, ErrAmbiguousAllocation
		}
		leader = job.AllocationID()
	}
	if leader == 0 {
		return Allocation{}, ErrAllocationMissing
	}
	jobs = slices.DeleteFunc(slices.Clone(jobs), func(job RegisteredJob) bool { return job.AllocationID() != leader })

	// Missing informer records must not be mistaken for unsubmitted children.
	ids := sets.New[string]()
	byGroup := make(map[string]RegisteredJob, len(jobs))
	composite := ir.RootPOM.Kind == "CompositePodGroup"
	completeSet := jobs[0].HetJobId == 0
	for _, job := range jobs {
		ids.Insert(strconv.Itoa(int(job.JobId)))
		key := ""
		if composite && job.info.PodGroup != nil {
			key = job.info.PodGroup.Key()
		}
		if _, exists := byGroup[key]; exists {
			return Allocation{}, ErrAmbiguousAllocation
		}
		byGroup[key] = job
	}
	for _, job := range jobs {
		if job.hetJobIDs == "" {
			continue
		}
		expected, err := hostlist.Expand("[" + strings.Trim(job.hetJobIDs, "[]") + "]")
		if err != nil {
			return Allocation{}, err
		}
		if !ids.HasAll(expected...) {
			return Allocation{}, ErrAllocationMissing
		}
		completeSet = true
	}

	selected := *ir
	selected.Components = nil
	allocation := Allocation{IR: &selected}
	membership := IndexAllocationPods(jobs)
	for _, component := range ir.Components {
		key := ""
		if composite {
			if group := externaljobinfo.NewGroupIdentity(component.ObjectMeta); group != nil {
				key = group.Key()
			}
		}
		job, found := byGroup[key]
		if !found {
			if !completeSet {
				return Allocation{}, ErrAllocationMissing
			}
			continue
		}
		pods := slices.DeleteFunc(slices.Clone(component.Pods.Items), func(p corev1.Pod) bool {
			return !membership.Contains(&p, job.JobId)
		})
		if len(pods) == 0 {
			continue
		}
		component.SelectPods(pods, len(job.info.Pods))
		selected.Components = append(selected.Components, component)
		allocation.Jobs = append(allocation.Jobs, job.ExternalJob)
	}
	if len(selected.Components) == 0 {
		return Allocation{}, ErrAllocationMembership
	}
	return allocation, nil
}
