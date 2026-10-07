// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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

type SlurmControlInterface interface {
	// RefreshJobCache forces the Node cache to be refreshed
	RefreshJobCache(ctx context.Context) error
	// ListPodsFromJobs returns a list of Slurm jobIds and their pods
	ListPodsFromJobs(ctx context.Context) ([]int32, []kubetypes.NamespacedName, error)
	// GetPodsFromJob returns a list of pod keys associated to the Slurm job.
	GetPodsFromJob(ctx context.Context, jobId int32) ([]kubetypes.NamespacedName, error)
	// IsJobPendingOrRunning returns true if the Slurm job with the given jobId is pending or running.
	IsJobPendingOrRunning(ctx context.Context, jobId int32) (bool, error)
	// TerminateJob cancels the Slurm job by JobId
	TerminateJob(ctx context.Context, jobId int32) error
	// TerminateHetJobComponent cancels only the component at hetJobOffset, including offset zero.
	TerminateHetJobComponent(ctx context.Context, hetJobId, hetJobOffset int32) error
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
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

// GetPodsFromJob implements SlurmControlInterface.
func (r *realSlurmControl) GetPodsFromJob(ctx context.Context, jobId int32) ([]kubetypes.NamespacedName, error) {
	job := &types.V0044JobInfo{}
	key := client.ObjectKey(fmt.Sprintf("%v", jobId))
	if err := r.Get(ctx, key, job); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

var _ SlurmControlInterface = &realSlurmControl{}

// TerminateHetJobComponent implements SlurmControlInterface.
func (r *realSlurmControl) TerminateHetJobComponent(ctx context.Context, hetJobId, hetJobOffset int32) error {
	if hetJobId <= 0 || hetJobOffset < 0 {
		return fmt.Errorf("invalid heterogeneous job component %d+%d", hetJobId, hetJobOffset)
	}
	raw := r.Versioned().V0044()
	if raw == nil {
		return errors.New("raw Slurm v0.0.44 client is unavailable")
	}
	selector := fmt.Sprintf("%d+%d", hetJobId, hetJobOffset)
	res, err := raw.SlurmV0044DeleteJobWithResponse(ctx, selector, &api.SlurmV0044DeleteJobParams{},
		func(_ context.Context, req *http.Request) error {
			req.URL.RawPath = strings.ReplaceAll(req.URL.EscapedPath(), "+", "%2B")
			return nil
		})
	if err != nil {
		return fmt.Errorf("terminate heterogeneous job component %s: %w", selector, err)
	}
	if res == nil || res.HTTPResponse == nil {
		return fmt.Errorf("terminate heterogeneous job component %s: missing HTTP response", selector)
	}
	if res.StatusCode() == http.StatusNotFound {
		return nil // Already absent, matching TerminateJob's idempotent behavior.
	}
	var errs []error
	if res.StatusCode() != http.StatusOK {
		errs = append(errs, fmt.Errorf("HTTP %d: %s", res.StatusCode(), http.StatusText(res.StatusCode())))
	}
	body := res.JSON200
	if body == nil {
		body = res.JSONDefault
	}
	if body == nil {
		errs = append(errs, errors.New("missing JSON cancellation response"))
	} else {
		for _, apiErr := range ptr.Deref(body.Errors, []api.V0044OpenapiError{}) {
			errs = append(errs, fmt.Errorf("slurm error %d (%s): %s: %s",
				ptr.Deref(apiErr.ErrorNumber, 0), ptr.Deref(apiErr.Source, ""),
				ptr.Deref(apiErr.Error, ""), ptr.Deref(apiErr.Description, "")))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("terminate heterogeneous job component %s: %w", selector, err)
	}
	return nil
}

func NewControl(client client.Client) SlurmControlInterface {
	return &realSlurmControl{
		Client: client,
	}
}
