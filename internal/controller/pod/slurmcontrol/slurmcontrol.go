// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type SlurmControlInterface interface {
	// IsJobRunning returns true if the Slurm job is running, false if not.
	IsJobRunning(ctx context.Context, pod *corev1.Pod) (bool, error)
	// IsJobPendingOrRunning returns true if the Slurm job with the given jobId is pending or running.
	IsJobPendingOrRunning(ctx context.Context, jobId int32) (bool, error)
	// TerminateJob cancels the Slurm job by JobId
	TerminateJob(ctx context.Context, jobId int32) error
}

// RealSlurmControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
}

// IsJobRunning implements SlurmControlInterface.
func (r *realSlurmControl) IsJobRunning(ctx context.Context, pod *corev1.Pod) (bool, error) {
	job := &types.V0044JobInfo{}
	jobId := object.ObjectKey(pod.Labels[wellknown.LabelPlaceholderJobId])
	if jobId == "" {
		return false, nil
	}
	err := r.Get(ctx, jobId, job, &client.GetOptions{RefreshCache: true})
	if err != nil {
		if tolerateError(err) {
			return false, nil
		}
		return false, err
	}
	if job.GetStateAsSet().Has(api.V0044JobInfoJobStateRUNNING) {
		return true, nil
	}
	return false, nil
}

// IsJobPendingOrRunning implements SlurmControlInterface.
func (r *realSlurmControl) IsJobPendingOrRunning(ctx context.Context, jobId int32) (bool, error) {
	job := &types.V0044JobInfo{}
	key := object.ObjectKey(fmt.Sprintf("%d", jobId))
	err := r.Get(ctx, key, job)
	if err != nil {
		if tolerateError(err) {
			return false, nil
		}
		return false, err
	}
	state := job.GetStateAsSet()
	return state.HasAny(api.V0044JobInfoJobStatePENDING, api.V0044JobInfoJobStateRUNNING), nil
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
	notFound := http.StatusText(http.StatusNotFound)
	noContent := http.StatusText(http.StatusNoContent)
	if strings.Contains(errText, notFound) || strings.Contains(errText, noContent) {
		return true
	}
	return false
}
