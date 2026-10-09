// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"

	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"
)

func (r *SlurmJobRunnable) Sync(ctx context.Context) error {
	if err := r.slurmControl.RefreshJobCache(ctx); err != nil {
		return err
	}

	jobIds, podKeys, err := r.slurmControl.ListPodsFromJobs(ctx)
	if err != nil {
		return err
	}

	for _, key := range podKeys {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: key.Namespace,
				Name:      key.Name,
			},
		}
		r.eventCh <- event.GenericEvent{Object: pod}
	}

	errs := []error{}
	for _, jobId := range jobIds {
		if err := r.cleanDanglingJob(ctx, jobId); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (r *SlurmJobRunnable) cleanDanglingJob(ctx context.Context, jobId int32) error {
	logger := log.FromContext(ctx)

	podKeys, err := r.slurmControl.GetPodsFromJob(ctx, jobId)
	if err != nil {
		return err
	}

	hasPods := false
	for _, podKey := range podKeys {
		pod := &corev1.Pod{}
		if err := r.Get(ctx, client.ObjectKey(podKey), pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		hasPods = true
		break
	}

	isPendingOrRunning, err := r.slurmControl.IsJobPendingOrRunning(ctx, jobId)
	if err != nil {
		return fmt.Errorf("failed to check if job is pending or running: %w", err)
	}

	if !hasPods && isPendingOrRunning {
		job := &types.V0044JobInfo{}
		if err := r.slurmClient.Get(ctx, object.ObjectKey(fmt.Sprintf("%d", jobId)), job); err != nil {
			if errors.Is(err, slurmerrors.ErrNotFound) {
				return nil
			}
			return err
		}
		logger.Info("Terminating Slurm Job, its Pods were deleted", "jobId", jobId)
		var err error
		switch {
		case job.HetJobId != nil && ptr.Deref(job.HetJobId.Set, false) && ptr.Deref(job.HetJobId.Number, 0) != 0:
			if job.HetJobOffset == nil || !ptr.Deref(job.HetJobOffset.Set, false) || job.HetJobOffset.Number == nil {
				return fmt.Errorf("missing heterogeneous job offset for job %d", jobId)
			}
			err = r.slurmControl.TerminateHetJobComponent(ctx, *job.HetJobId.Number, *job.HetJobOffset.Number)
		default:
			err = r.slurmControl.TerminateJob(ctx, jobId)
		}
		if err != nil {
			return fmt.Errorf("failed to terminate jobId(%d): %w", jobId, err)
		}
	}

	return nil
}
