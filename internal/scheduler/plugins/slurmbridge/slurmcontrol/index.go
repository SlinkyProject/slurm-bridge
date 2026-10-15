// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
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

const podJobIndex = "bridge.pod"

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

// GetJobsForPods returns cached candidates for the requested Pods.
// Ambiguous matches are omitted unless the Pod's job-ID label selects one.
// Verify candidates before repairing metadata or deciding a handoff.
func (r *realSlurmControl) GetJobsForPods(ctx context.Context, pods ...*corev1.Pod) (*map[string]ExternalJob, error) {
	podToJob := make(map[string]ExternalJob, len(pods))
	for _, pod := range pods {
		key := externaljobinfo.PodKey(pod)
		jobs := &api.V0044JobInfoObjectList{}
		if err := r.jobInformer.ByIndex(ctx, podJobIndex, key, jobs); err != nil {
			return nil, err
		}
		if len(jobs.Items) == 0 && pod.UID != "" {
			if err := r.jobInformer.ByIndex(ctx, podJobIndex, pod.Namespace+"/"+pod.Name, jobs); err != nil {
				return nil, err
			}
		}
		jobID := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
		var candidate ExternalJob
		matches := 0
		for i := range jobs.Items {
			job, err := externalJobFromJobInfo(&jobs.Items[i])
			if err != nil {
				continue
			}
			candidate = job
			matches++
			if job.JobId == jobID {
				matches = 1
				break
			}
		}
		if matches == 1 {
			podToJob[key] = candidate
		}
	}
	return &podToJob, nil
}

// VerifyJobsForPods reads candidates directly from Slurm, confirming ownership
// and state without replacing another live allocation. Shared jobs are read once.
func (r *realSlurmControl) VerifyJobsForPods(ctx context.Context, candidates map[string]ExternalJob, pods ...*corev1.Pod) (*map[string]ExternalJob, error) {
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
		extJob, err := externalJobFromJobInfo(job)
		if err != nil || extJob.JobId != candidateID {
			continue
		}
		podToJob[key] = extJob
	}
	return &podToJob, nil
}
