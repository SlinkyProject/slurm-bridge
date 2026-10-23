// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const PodJobIndex = "bridge.pod"
const GroupJobIndex = "bridge.group"
const LegacyPodJobIndex = "bridge.legacy-pod"

// PodIndex provides cached Pod ownership lookups and explicit live verification.
type PodIndex struct {
	client.Client
	jobInformer client.InformerCache
}

func NewPodIndex(slurmClient client.Client, jobInformer client.InformerCache) (*PodIndex, error) {
	if err := jobInformer.AddIndex(PodJobIndex, jobPodKeys); err != nil {
		return nil, fmt.Errorf("register Slurm job index: %w", err)
	}
	if err := jobInformer.AddIndex(GroupJobIndex, jobGroupKeys); err != nil {
		return nil, fmt.Errorf("register Slurm group index: %w", err)
	}
	if err := jobInformer.AddIndex(LegacyPodJobIndex, jobLegacyPodKeys); err != nil {
		return nil, fmt.Errorf("register legacy Slurm Pod index: %w", err)
	}
	return &PodIndex{Client: slurmClient, jobInformer: jobInformer}, nil
}

// jobLegacyPodKeys isolates allocations without group metadata, so compatibility
// checks do not repeatedly copy a modern gang's allocation for every member.
func jobLegacyPodKeys(obj object.Object) []string {
	info := externaljobinfo.ExternalJobInfo{}
	if err := externaljobinfo.ParseIntoExternalJobInfo(obj.(*api.V0044JobInfo).AdminComment, &info); err != nil {
		return nil
	}
	if info.PodGroup != nil || info.CompositePodGroup != nil {
		return nil
	}
	return info.Pods
}

func jobGroupKeys(obj object.Object) []string {
	info := externaljobinfo.ExternalJobInfo{}
	if err := externaljobinfo.ParseIntoExternalJobInfo(obj.(*api.V0044JobInfo).AdminComment, &info); err != nil {
		return nil
	}
	var keys []string
	for _, group := range []*externaljobinfo.GroupIdentity{info.PodGroup, info.CompositePodGroup} {
		if group != nil {
			keys = append(keys, group.Key())
		}
	}
	return keys
}

// GetAllocation resolves every component of a single active Kubernetes allocation.
// Pod labels never select between conflicting allocations.
func (r *PodIndex) GetAllocation(ctx context.Context, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]ExternalJob, error) {
	identity, err := IdentityForAllocation(pod, ir)
	if err != nil {
		return nil, err
	}
	jobs, err := IndexedAllocation(ctx, r.jobInformer, identity)
	if err != nil {
		return nil, err
	}
	components, complete, err := MatchAllocationComponents(ir, jobs)
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, ErrAllocationMissing
	}
	return components, nil
}

func jobPodKeys(obj object.Object) []string {
	job, ok := obj.(*api.V0044JobInfo)
	if !ok {
		return nil
	}
	info := externaljobinfo.ExternalJobInfo{}
	if err := externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, &info); err != nil {
		return nil
	}
	return info.Pods
}

// JobForPod matches a pod's UID, falling back to namespace/name for legacy jobs.
func JobForPod(jobs map[string]ExternalJob, pod *corev1.Pod) (ExternalJob, bool) {
	name := pod.Namespace + "/" + pod.Name
	if job, ok := jobs[externaljobinfo.PodKey(pod)]; ok {
		return job, true
	}
	job, ok := jobs[name]
	return job, ok
}

// GetJobsForPods returns cached candidates for the requested Pods. Active matches
// must be unique; neither the Pod label nor index order resolves a conflict.
// Callers must verify candidates before repairing metadata or deciding a handoff.
func (r *PodIndex) GetJobsForPods(ctx context.Context, pods ...*corev1.Pod) (*map[string]ExternalJob, error) {
	podToJob := make(map[string]ExternalJob, len(pods))
	for _, pod := range pods {
		key := externaljobinfo.PodKey(pod)
		jobs := &api.V0044JobInfoObjectList{}
		if err := r.jobInformer.ByIndex(ctx, PodJobIndex, key, jobs); err != nil {
			return nil, err
		}
		if len(jobs.Items) == 0 && pod.UID != "" {
			if err := r.jobInformer.ByIndex(ctx, PodJobIndex, pod.Namespace+"/"+pod.Name, jobs); err != nil {
				return nil, err
			}
		}
		jobID := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
		var candidate ExternalJob
		var finished ExternalJob
		finishedMatches := 0
		matches := 0
		for i := range jobs.Items {
			job, err := FromJobInfo(&jobs.Items[i])
			if err != nil {
				continue
			}
			if job.Finished {
				finishedMatches++
				if finished.JobId == 0 || job.JobId == jobID {
					finished = job
				}
				continue
			}
			candidate = job
			matches++
		}
		if matches == 1 {
			podToJob[key] = candidate
		} else if matches == 0 && (finishedMatches == 1 || finished.JobId == jobID && jobID > 0) {
			podToJob[key] = finished
		}
	}
	return &podToJob, nil
}

// VerifyJobsForPods reads candidates directly from Slurm, confirming ownership
// and state without replacing another live allocation. Shared jobs are read once.
func (r *PodIndex) VerifyJobsForPods(ctx context.Context, candidates map[string]ExternalJob, pods ...*corev1.Pod) (*map[string]ExternalJob, error) {
	podToJob := make(map[string]ExternalJob, len(pods))
	liveJobs := make(map[int32]*api.V0044JobInfo)
	readJob := func(id int32) (*api.V0044JobInfo, error) {
		if job, read := liveJobs[id]; read {
			return job, nil
		}
		job := &api.V0044JobInfo{}
		err := r.Get(ctx, object.ObjectKey(strconv.Itoa(int(id))), job, &client.GetOptions{SkipCache: true})
		if errors.Is(err, slurmerrors.ErrNotFound) {
			job = nil
		} else if err != nil {
			return nil, err
		}
		liveJobs[id] = job
		return job, nil
	}
	for _, pod := range pods {
		key := externaljobinfo.PodKey(pod)
		candidate, ok := JobForPod(candidates, pod)
		if !ok {
			continue
		}
		candidateID := candidate.JobId
		jobID := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
		if jobID > 0 && candidateID != jobID {
			// A recently created allocation may not be indexed yet. An old job
			// claiming the same Pod name cannot replace a live labeled allocation.
			labeled, err := readJob(jobID)
			if err != nil {
				return nil, err
			}
			if labeled != nil && (ptr.Deref(labeled.JobId, 0) != jobID || !isFinishedJob(labeled)) {
				continue
			}
		}
		job, err := readJob(candidateID)
		if err != nil {
			return nil, err
		}
		if job == nil {
			continue
		}
		keys := jobPodKeys(job)
		if !slices.Contains(keys, key) && !slices.Contains(keys, pod.Namespace+"/"+pod.Name) {
			continue
		}
		extJob, err := FromJobInfo(job)
		if err != nil || extJob.JobId != candidateID {
			continue
		}
		podToJob[key] = extJob
	}
	return &podToJob, nil
}
