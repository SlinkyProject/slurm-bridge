// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	podv1 "k8s.io/kubernetes/pkg/api/v1/pod"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// podReadyForCleanup includes deleting, unbound Pods: binding is forbidden once
// deletion starts, and no kubelet can mark them terminal. Bound Pods must finish.
func podReadyForCleanup(pod *corev1.Pod) bool {
	return podv1.IsPodTerminal(pod) || (pod.DeletionTimestamp != nil && pod.Spec.NodeName == "")
}

// syncPod repairs linkage before checking whether a bound Pod can still run.
// A successful patch must be observed by the cache before cleanup proceeds.
func (r *Reconciler) syncPod(ctx context.Context, req reconcile.Request) (bool, error) {
	changed, err := r.reconcilePodMetadata(ctx, req)
	if err != nil || changed {
		return changed, err
	}
	return false, r.syncKubernetes(ctx, req)
}

// syncKubernetes reconciles the Kubernetes Pod with Slurm Jobs.
// It will terminate the pod without a corresponding job.
func (r *Reconciler) syncKubernetes(ctx context.Context, req reconcile.Request) error {
	logger := log.FromContext(ctx)
	podKey := req.String()

	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if pod.Spec.SchedulerName != r.schedulerName {
		logger.V(2).Info("Pod is not scheduled by the slurm-bridge, skipping",
			"pod", klog.KObj(pod), "scheduler", r.schedulerName)
		return nil
	}

	// Terminal pods are handled by prepareTerminalPod
	if podReadyForCleanup(pod) {
		logger.V(2).Info("Pod is terminal, skipping", "pod", klog.KObj(pod))
		return nil
	}

	// Unbound pods must be scheduled before checking if the Slurm job is running
	if pod.Spec.NodeName == "" {
		logger.V(2).Info("Pod is not bound, skipping", "pod", klog.KObj(pod))
		return nil
	}

	jobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
	exists, err := r.slurmControl.IsJobRunning(ctx, pod)
	if err != nil {
		logger.Error(err, "failed to fetch Slurm job information", "jobId", jobId)
		return err
	}

	if !exists {
		logger.Info("Deleting Pod for corresponding Slurm Job",
			"pod", podKey, "jobId", jobId)
		if err := r.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}); err != nil {
			logger.Error(err, "failed to terminate Pod without corresponding Slurm Job",
				"pod", podKey, "jobId", jobId)
			return err
		}
	}

	return nil
}

// prepareTerminalPod deletes the Pod's claims before removing its scheduler
// finalizer. Call it only after allocation release is confirmed.
func (r *Reconciler) prepareTerminalPod(ctx context.Context, req reconcile.Request, expected ...*corev1.Pod) error {
	logger := log.FromContext(ctx)
	podKey := req.String()

	pod := &corev1.Pod{}
	reader := client.Reader(r.Client)
	if r.apiReader != nil {
		reader = r.apiReader
	}
	if err := reader.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(2).Info("Pod not found, no finalizer to remove", "pod", podKey)
			return nil
		}
		return err
	}

	if len(expected) > 0 && (pod.UID != expected[0].UID || pod.ResourceVersion != expected[0].ResourceVersion) {
		return fmt.Errorf("pod changed before finalization: %s", req)
	}
	if !podReadyForCleanup(pod) {
		logger.V(2).Info("Pod is not terminated, skipping", "pod", podKey)
		return nil
	}

	// Find partial claims even if the Pod status update failed.
	claims := &resourcev1.ResourceClaimList{}
	if err := r.List(ctx, claims, client.InNamespace(pod.Namespace), client.MatchingLabels{wellknown.LabelAllocationPodUID: string(pod.UID)}); err != nil {
		return err
	}
	for i := range claims.Items {
		claim := &claims.Items[i]
		owned := false
		for _, owner := range claim.OwnerReferences {
			if owner.Kind == "Pod" && owner.UID == pod.UID {
				owned = true
			}
		}
		if owned {
			if err := r.Delete(ctx, claim, client.Preconditions{UID: &claim.UID, ResourceVersion: &claim.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	if pod.Status.ExtendedResourceClaimStatus != nil &&
		pod.Status.ExtendedResourceClaimStatus.ResourceClaimName != "" {
		claimKey := client.ObjectKey{
			Name:      pod.Status.ExtendedResourceClaimStatus.ResourceClaimName,
			Namespace: pod.Namespace,
		}
		var claim resourcev1.ResourceClaim
		if err := r.Get(ctx, claimKey, &claim); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to get resource claim", "claimKey", claimKey)
				return err
			}
		} else {
			if err := r.Delete(ctx, &claim, client.Preconditions{UID: &claim.UID, ResourceVersion: &claim.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to delete resource claim", "claim", claim)
				return err
			}
		}
	}

	finalizers := []string{}
	for _, f := range pod.Finalizers {
		if f != wellknown.FinalizerScheduler {
			finalizers = append(finalizers, f)
		}
	}
	if len(finalizers) == len(pod.Finalizers) {
		return nil
	}
	toUpdate := pod.DeepCopy()
	toUpdate.Finalizers = finalizers
	if err := r.Patch(ctx, toUpdate, client.StrategicMergeFrom(pod, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		logger.Error(err, "failed to remove finalizer", "pod", podKey)
		return err
	}

	return nil
}
