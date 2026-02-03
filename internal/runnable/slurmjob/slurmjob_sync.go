// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

	return utilerrors.NewAggregate(errs)
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
			if errors.IsNotFound(err) {
				continue
			}
			return err
		}
		hasPods = true
		break
	}

	if !hasPods {
		logger.Info("Terminating Slurm Job, its Pods were deleted", "jobId", jobId)
		if err := r.slurmControl.TerminateJob(ctx, jobId); err != nil {
			return fmt.Errorf("failed to terminate jobId(%d): %w", jobId, err)
		}
	}

	return nil
}
