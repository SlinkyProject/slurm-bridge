// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func isPodGroupRoot(pom *metav1.PartialObjectMetadata) bool {
	return pom.APIVersion == "scheduling.k8s.io/v1alpha2" && pom.Kind == "PodGroup"
}

func podsHaveSlurmNodeAssignments(pods *corev1.PodList, jobID string) bool {
	if len(pods.Items) == 0 || jobID == "" {
		return false
	}
	for _, p := range pods.Items {
		if p.Labels[wellknown.LabelExternalJobId] != jobID ||
			p.Annotations[wellknown.AnnotationExternalJobNode] == "" {
			return false
		}
	}
	return true
}

// markPodGroupScheduled sets PodGroupScheduled=True so kubectl shows STATUS Scheduled.
// kube-scheduler normally writes this when it admits a gang; slurm-bridge must do the same.
func (sb *SlurmBridge) markPodGroupScheduled(ctx context.Context, slurmJobIR *slurmjobir.SlurmJobIR, jobID string) {
	if slurmJobIR == nil || !isPodGroupRoot(&slurmJobIR.RootPOM) || jobID == "" {
		return
	}
	if err := sb.refreshSlurmJobIRPods(ctx, slurmJobIR); err != nil {
		klog.FromContext(ctx).V(4).Info("skip PodGroup status update", "err", err)
		return
	}
	if !podsHaveSlurmNodeAssignments(&slurmJobIR.Pods, jobID) {
		return
	}

	logger := klog.FromContext(ctx)
	pg := &schedulingv1alpha2.PodGroup{}
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.Namespace, Name: slurmJobIR.RootPOM.Name}
	if err := sb.Get(ctx, key, pg); err != nil {
		logger.V(4).Info("skip PodGroup status update", "podGroup", key, "err", err)
		return
	}
	if cond := apimeta.FindStatusCondition(pg.Status.Conditions, schedulingv1alpha2.PodGroupScheduled); cond != nil && cond.Status == metav1.ConditionTrue {
		return
	}

	toUpdate := pg.DeepCopy()
	now := metav1.Now()
	apimeta.SetStatusCondition(&toUpdate.Status.Conditions, metav1.Condition{
		Type:               schedulingv1alpha2.PodGroupScheduled,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: pg.Generation,
		LastTransitionTime: now,
		Reason:             "Scheduled",
		Message:            "Pod group admitted by " + sb.schedulerName,
	})
	if err := sb.Status().Patch(ctx, toUpdate, client.StrategicMergeFrom(pg)); err != nil {
		logger.Error(err, "failed to patch PodGroup status", "podGroup", key)
		return
	}
	logger.V(4).Info("marked PodGroup scheduled", "podGroup", key)
}

func (sb *SlurmBridge) refreshSlurmJobIRPods(ctx context.Context, slurmJobIR *slurmjobir.SlurmJobIR) error {
	for i := range slurmJobIR.Pods.Items {
		key := client.ObjectKeyFromObject(&slurmJobIR.Pods.Items[i])
		if err := sb.Get(ctx, key, &slurmJobIR.Pods.Items[i]); err != nil {
			return err
		}
	}
	return nil
}
