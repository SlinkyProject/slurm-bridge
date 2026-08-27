// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"

	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
)

// BridgeJob is a bridge-created external job and the pods it claims via its
// admin comment.
type BridgeJob struct {
	JobId   int32
	Nodes   string
	PodKeys []kubetypes.NamespacedName
}

type SlurmControlInterface interface {
	// RefreshJobCache forces the Node cache to be refreshed
	RefreshJobCache(ctx context.Context) error
	// ListPodsFromJobs returns a list of Slurm jobIds and their pods
	ListPodsFromJobs(ctx context.Context) ([]int32, []kubetypes.NamespacedName, error)
	// ListBridgeJobs returns every bridge-created job with its node list and
	// claimed pods, served from the job cache.
	ListBridgeJobs(ctx context.Context) ([]BridgeJob, error)
	// GetJobNodesLive reports whether the job exists in a non-terminal state
	// and which nodes it holds, bypassing the job cache.
	GetJobNodesLive(ctx context.Context, jobId int32) (exists bool, nodes string, err error)
	// GetPodsFromJob returns a list of pod keys associated to the Slurm job.
	GetPodsFromJob(ctx context.Context, jobId int32) ([]kubetypes.NamespacedName, error)
	// IsJobPendingOrRunning returns true if the Slurm job with the given jobId is pending or running.
	IsJobPendingOrRunning(ctx context.Context, jobId int32) (bool, error)
	// TerminateJob cancels the Slurm job by JobId
	TerminateJob(ctx context.Context, jobId int32) error
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
}

// RefreshJobCache implements SlurmControlInterface.
func (r *realSlurmControl) RefreshJobCache(ctx context.Context) error {
	jobList := &types.V0044JobInfoList{}
	opts := &client.ListOptions{
		RefreshCache: true,
	}
	if err := r.List(ctx, jobList, opts); err != nil {
		return err
	}
	return nil
}

// IsJobPendingOrRunning implements SlurmControlInterface.
func (r *realSlurmControl) IsJobPendingOrRunning(ctx context.Context, jobId int32) (bool, error) {
	job := &types.V0044JobInfo{}
	key := object.ObjectKey(fmt.Sprintf("%d", jobId))
	err := r.Get(ctx, key, job)
	if err != nil {
		if errors.Is(err, slurmerrors.ErrObjectNotFound) {
			return false, nil
		}
		return false, err
	}
	state := job.GetStateAsSet()
	return state.HasAny(api.V0044JobInfoJobStatePENDING, api.V0044JobInfoJobStateRUNNING), nil
}

// ListPodsFromJobs implements SlurmControlInterface.
func (r *realSlurmControl) ListPodsFromJobs(ctx context.Context) ([]int32, []kubetypes.NamespacedName, error) {
	jobList := &types.V0044JobInfoList{}
	if err := r.List(ctx, jobList); err != nil {
		return nil, nil, err
	}

	jobIds := []int32{}
	pods := []kubetypes.NamespacedName{}
	for _, job := range jobList.Items {
		extInfo := &externaljobinfo.ExternalJobInfo{}
		if err := externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, extInfo); err != nil {
			// Assume the job was not created by slurm-bridge
			continue
		}
		jobId := ptr.Deref(job.JobId, 0)
		jobIds = append(jobIds, jobId)
		for _, podName := range extInfo.Pods {
			pods = append(pods, utils.NamespacedNameFromString(podName))
		}
	}

	return jobIds, pods, nil
}

// ListBridgeJobs implements SlurmControlInterface.
func (r *realSlurmControl) ListBridgeJobs(ctx context.Context) ([]BridgeJob, error) {
	jobList := &types.V0044JobInfoList{}
	if err := r.List(ctx, jobList); err != nil {
		return nil, err
	}

	jobs := []BridgeJob{}
	for _, job := range jobList.Items {
		extInfo := &externaljobinfo.ExternalJobInfo{}
		if err := externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, extInfo); err != nil {
			// Assume the job was not created by slurm-bridge
			continue
		}
		out := BridgeJob{
			JobId: ptr.Deref(job.JobId, 0),
			Nodes: ptr.Deref(job.Nodes, ""),
		}
		for _, podName := range extInfo.Pods {
			out.PodKeys = append(out.PodKeys, utils.NamespacedNameFromString(podName))
		}
		jobs = append(jobs, out)
	}

	return jobs, nil
}

// GetJobNodesLive implements SlurmControlInterface.
func (r *realSlurmControl) GetJobNodesLive(ctx context.Context, jobId int32) (bool, string, error) {
	if jobId == 0 {
		return false, "", nil
	}
	job := &types.V0044JobInfo{}
	key := object.ObjectKey(fmt.Sprintf("%d", jobId))
	if err := r.Get(ctx, key, job, &client.GetOptions{SkipCache: true}); err != nil {
		if utils.IsSlurmJobNotFoundErr(err) {
			return false, "", nil
		}
		return false, "", err
	}
	if job.GetStateAsSet().HasAny(api.V0044JobInfoJobStateCANCELLED, api.V0044JobInfoJobStateCOMPLETED) {
		return false, "", nil
	}
	return true, ptr.Deref(job.Nodes, ""), nil
}

// GetPodsFromJob implements SlurmControlInterface.
func (r *realSlurmControl) GetPodsFromJob(ctx context.Context, jobId int32) ([]kubetypes.NamespacedName, error) {
	job := &types.V0044JobInfo{}
	key := client.ObjectKey(fmt.Sprintf("%v", jobId))
	if err := r.Get(ctx, key, job); err != nil {
		if errors.Is(err, slurmerrors.ErrObjectNotFound) {
			return nil, nil
		}
		return nil, err
	}

	extInfo := &externaljobinfo.ExternalJobInfo{}
	if err := externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, extInfo); err != nil {
		// Assume the job was not created by slurm-bridge
		return nil, nil //nolint:nilerr
	}

	podKeys := []kubetypes.NamespacedName{}
	for _, podName := range extInfo.Pods {
		podKeys = append(podKeys, utils.NamespacedNameFromString(podName))
	}

	return podKeys, nil
}

// TerminateJob implements SlurmControlInterface.
func (r *realSlurmControl) TerminateJob(ctx context.Context, jobId int32) error {
	job := &types.V0044JobInfo{
		V0044JobInfo: api.V0044JobInfo{
			JobId: ptr.To(jobId),
		},
	}
	if err := r.Delete(ctx, job); err != nil {
		if errors.Is(err, slurmerrors.ErrObjectNotFound) {
			return nil
		}
		return err
	}
	return nil
}

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(client client.Client) SlurmControlInterface {
	return &realSlurmControl{
		Client: client,
	}
}
