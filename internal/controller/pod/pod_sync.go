// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"context"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	podv1 "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/test/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *PodReconciler) Sync(ctx context.Context, req reconcile.Request) error {
	var errs []error

	if err := r.syncKubernetes(ctx, req); err != nil {
		errs = append(errs, err)
	}

	if err := r.syncSlurm(ctx, req); err != nil {
		errs = append(errs, err)
	}

	if err := r.deleteFinalizer(ctx, req); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// syncKubernetes reconciles the Kubernetes Pod with Slurm Jobs.
// It will terminate the pod without a corresponding job.
func (r *PodReconciler) syncKubernetes(ctx context.Context, req reconcile.Request) error {
	logger := log.FromContext(ctx)
	podKey := req.String()

	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if pod.Spec.SchedulerName != r.SchedulerName {
		logger.V(2).Info("Pod is not scheduled by the slurm-bridge, skipping",
			"pod", klog.KObj(pod), "scheduler", r.SchedulerName)
		return nil
	}

	if active, _ := utils.PodRunningReady(pod); !active {
		logger.V(2).Info("Pod is not running, skipping", "pod", klog.KObj(pod))
		return nil
	}

	jobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelPlaceholderJobId])
	exists, err := r.slurmControl.IsJobRunning(ctx, pod)
	if err != nil {
		logger.Error(err, "failed to fetch Slurm job information", "jobId", jobId)
		return err
	}

	if !exists {
		logger.Info("Deleting Pod for corresponding Slurm Job",
			"pod", podKey, "jobId", jobId)
		if err := r.Delete(ctx, pod); err != nil {
			logger.Error(err, "failed to terminate Pod without corresponding Slurm Job",
				"pod", podKey, "jobId", jobId)
			return err
		}
	}

	return nil
}

// syncSlurm reconciles the Slurm Job with Kubernetes Pods.
// It will terminate the job corresponding to a terminat[ed,ing] pod.
func (r *PodReconciler) syncSlurm(ctx context.Context, req reconcile.Request) error {
	logger := log.FromContext(ctx)
	podKey := req.String()

	notFound := false
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			notFound = true
		} else {
			return err
		}
	}

	if notFound {
		logger.V(2).Info("Pod not found, no Job ID", "pod", podKey)
		return nil
	}

	if pod.DeletionTimestamp == nil && !podv1.IsPodTerminal(pod) {
		logger.V(2).Info("Pod is not terminated or terminating, skipping", "pod", podKey)
		return nil
	}

	pods := &corev1.PodList{}
	if err := r.List(context.Background(), pods,
		&client.ListOptions{LabelSelector: labels.SelectorFromSet(
			labels.Set{wellknown.LabelPlaceholderJobId: pod.Labels[wellknown.LabelPlaceholderJobId]},
		)}); err != nil {
		logger.Error(err, "failed to fetch pods associated with Slurm job")
		return err
	}

	// If there are no pods labeled with this jobId the Slurm Job may
	// be terminated.
	activePods := 0
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil && !podv1.IsPodTerminal(&p) {
			activePods++
		}
	}
	if activePods == 0 {
		jobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelPlaceholderJobId])
		logger.Info("Terminate Slurm Job for Pod", "pod", klog.KObj(pod), "jobId", jobId)
		if err := r.slurmControl.TerminateJob(ctx, jobId); err != nil {
			logger.Error(err, "failed to terminate Slurm Job without corresponding Pod",
				"jobId", jobId, "pod", podKey)
			return err
		}
	}

	return nil
}

// deleteFinalizer will remove the finalizer from the pod if it is to be deleted.
// This is done to ensure syncSlurm is able to get the pod labels to determine
// if the pod has a placeholder JobId.
func (r *PodReconciler) deleteFinalizer(ctx context.Context, req reconcile.Request) error {
	logger := log.FromContext(ctx)
	podKey := req.String()

	notFound := false
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			notFound = true
		} else {
			return err
		}
	}

	if notFound {
		logger.V(2).Info("Pod not found, no finalizer to remove", "pod", podKey)
		return nil
	}

	if pod.DeletionTimestamp == nil && !podv1.IsPodTerminal(pod) {
		logger.V(2).Info("Pod is not terminated or terminating, skipping", "pod", podKey)
		return nil
	}

	finalizers := []string{}
	for _, f := range pod.Finalizers {
		if f != wellknown.FinalizerScheduler {
			finalizers = append(finalizers, f)
		}
	}
	toUpdate := pod.DeepCopy()
	toUpdate.Finalizers = finalizers
	if err := r.Patch(ctx, toUpdate, client.StrategicMergeFrom(pod)); err != nil {
		logger.Error(err, "failed to remove finalizer", "pod", podKey)
		return err
	}

	return nil
}
