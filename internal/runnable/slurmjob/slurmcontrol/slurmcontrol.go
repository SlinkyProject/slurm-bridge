// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"fmt"
	"net/http"

	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
)

type SlurmControlInterface interface {
	// RefreshJobCache forces the Node cache to be refreshed
	RefreshJobCache(ctx context.Context) error
	// ListPodsFromJobs returns a list of Slurm jobIds and their pods
	ListPodsFromJobs(ctx context.Context) ([]int32, []kubetypes.NamespacedName, error)
	// GetPodsFromJob returns a list of pod keys associated to the Slurm job.
	GetPodsFromJob(ctx context.Context, jobId int32) ([]kubetypes.NamespacedName, error)
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
		if tolerateError(err) {
			return nil
		}
		return err
	}
	return nil
}

// ListPodsFromJobs implements SlurmControlInterface.
func (r *realSlurmControl) ListPodsFromJobs(ctx context.Context) ([]int32, []kubetypes.NamespacedName, error) {
	jobList := &types.V0044JobInfoList{}
	if err := r.List(ctx, jobList); err != nil {
		if tolerateError(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	jobIds := []int32{}
	pods := []kubetypes.NamespacedName{}
	for _, job := range jobList.Items {
		phInfo := &placeholderinfo.PlaceholderInfo{}
		if err := placeholderinfo.ParseIntoPlaceholderInfo(job.AdminComment, phInfo); err != nil {
			// Assume the job was not created by slurm-bridge
			continue
		}
		jobId := ptr.Deref(job.JobId, 0)
		jobIds = append(jobIds, jobId)
		for _, podName := range phInfo.Pods {
			pods = append(pods, utils.NamespacedNameFromString(podName))
		}
	}

	return jobIds, pods, nil
}

// GetPodsFromJob implements SlurmControlInterface.
func (r *realSlurmControl) GetPodsFromJob(ctx context.Context, jobId int32) ([]kubetypes.NamespacedName, error) {
	job := &types.V0044JobInfo{}
	key := client.ObjectKey(fmt.Sprintf("%v", jobId))
	if err := r.Get(ctx, key, job); err != nil {
		if tolerateError(err) {
			return nil, nil
		}
		return nil, err
	}

	phInfo := &placeholderinfo.PlaceholderInfo{}
	if err := placeholderinfo.ParseIntoPlaceholderInfo(job.AdminComment, phInfo); err != nil {
		// Assume the job was not created by slurm-bridge
		return nil, nil //nolint:nilerr
	}

	podKeys := []kubetypes.NamespacedName{}
	for _, podName := range phInfo.Pods {
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
		if tolerateError(err) {
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

func tolerateError(err error) bool {
	if err == nil {
		return true
	}
	errText := err.Error()
	if errText == http.StatusText(http.StatusNotFound) ||
		errText == http.StatusText(http.StatusNoContent) {
		return true
	}
	return false
}
