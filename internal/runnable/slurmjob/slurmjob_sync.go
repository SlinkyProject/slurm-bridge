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

	slurmapi "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"
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

	podRefs, err := r.slurmControl.GetPodsFromJob(ctx, jobId)
	if err != nil {
		return err
	}

	for _, ref := range podRefs {
		pod := &corev1.Pod{}
		key := client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
		err := r.Get(ctx, key, pod)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err != nil || (ref.UID != "" && ref.UID != pod.UID) {
			// Slurm and Kubernetes caches refresh independently. Confirm a missing
			// or replaced pod through the API before canceling its allocation.
			pod = &corev1.Pod{}
			if err := r.apiReader.Get(ctx, key, pod); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to confirm pod %s for job %d: %w", key, jobId, err)
			}
		}
		if ref.UID != "" && ref.UID != pod.UID {
			continue
		}
		return nil
	}

	jobIsPending, jobIsRunning, err := r.slurmControl.IsJobPendingOrRunning(ctx, jobId)
	if err != nil {
		return fmt.Errorf("failed to check if job is pending or running: %w", err)
	}

	if !jobIsPending && !jobIsRunning {
		return nil
	}

	job := &slurmapi.V0044JobInfo{}
	if err := r.slurmClient.Get(ctx, object.ObjectKey(fmt.Sprintf("%d", jobId)), job); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return nil
		}
		return err
	}
	logger.Info("Terminating Slurm Job, its Pods were deleted", "jobId", jobId)

	switch {
	case job.HetJobId != nil && ptr.Deref(job.HetJobId.Set, false) && ptr.Deref(job.HetJobId.Number, 0) != 0:
		if job.HetJobOffset == nil || !ptr.Deref(job.HetJobOffset.Set, false) || job.HetJobOffset.Number == nil {
			return fmt.Errorf("missing heterogeneous job offset for job %d", jobId)
		}

		if jobIsRunning {
			return r.slurmControl.TerminateHetJobComponent(ctx, *job.HetJobId.Number, *job.HetJobOffset.Number)
		}

		jobId = *job.HetJobId.Number
		fallthrough
	default:
		err = r.slurmControl.TerminateJob(ctx, jobId)
	}
	if err != nil {
		return fmt.Errorf("failed to terminate jobId(%d): %w", jobId, err)
	}

	return nil
}
