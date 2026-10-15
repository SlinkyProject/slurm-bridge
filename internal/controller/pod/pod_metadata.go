// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"context"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	podv1 "k8s.io/kubernetes/pkg/api/v1/pod"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
)

// reconcilePodMetadata repairs existing job linkage before cleanup can act on it.
func (r *PodReconciler) reconcilePodMetadata(ctx context.Context, req reconcile.Request) (bool, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if pod.Spec.SchedulerName != r.SchedulerName || pod.DeletionTimestamp != nil || podv1.IsPodTerminal(pod) {
		return false, nil
	}
	candidates, err := r.jobIndex.GetJobsForPods(ctx, pod)
	if err != nil {
		return false, err
	}
	job, ok := slurmjob.JobForPod(*candidates, pod)
	if !ok || reflect.DeepEqual(pod, slurmjob.PodWithMetadata(ctx, pod, job, false)) {
		return false, nil
	}
	// An index miss is not authoritative. Only a proposed repair needs live
	// ownership and allocation verification, including the current labeled job.
	verified, err := r.jobIndex.VerifyJobsForPods(ctx, *candidates, pod)
	if err != nil {
		return false, err
	}
	job, ok = slurmjob.JobForPod(*verified, pod)
	if !ok {
		return false, nil
	}
	toUpdate := slurmjob.PodWithMetadata(ctx, pod, job, false)
	if reflect.DeepEqual(pod, toUpdate) {
		return false, nil
	}
	// The scheduler may have assigned or bound the Pod during the Slurm read.
	if err := r.Patch(ctx, toUpdate, client.StrategicMergeFrom(pod, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	log.FromContext(ctx).V(3).Info("Repaired Pod metadata", "pod", req.NamespacedName, "jobId", job.JobId)
	return true, nil
}
