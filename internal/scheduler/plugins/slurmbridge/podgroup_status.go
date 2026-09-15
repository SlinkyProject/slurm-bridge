// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

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

// markPodGroupScheduled sets the served API version's scheduled condition to True.
// kube-scheduler normally writes this when it admits a gang; slurm-bridge must do the same.
func (sb *SlurmBridge) markPodGroupScheduled(ctx context.Context, slurmJobIR *slurmjobir.SlurmJobIR, slurmJobComponent *slurmjobir.SlurmJobComponent, jobID string) {
	if slurmJobIR == nil || slurmJobComponent == nil || sb.workloadAPI == nil || slurmJobIR.RootPOM.TypeMeta != sb.workloadAPI.PodGroupTypeMeta || jobID == "" {
		return
	}

	logger := klog.FromContext(ctx)
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.Namespace, Name: slurmJobIR.RootPOM.Name}
	pg := &slurmjobir.PodGroup{TypeMeta: sb.workloadAPI.PodGroupTypeMeta}
	if err := sb.Get(ctx, key, pg); err != nil {
		logger.V(4).Info("skip PodGroup status update", "podGroup", key, "err", err)
		return
	}
	conditionType := sb.workloadAPI.ScheduledCondition
	if cond := apimeta.FindStatusCondition(pg.Status.Conditions, conditionType); cond != nil && cond.Status == metav1.ConditionTrue {
		return
	}

	if err := sb.refreshSlurmJobIRPods(ctx, slurmJobIR); err != nil {
		logger.V(4).Info("skip PodGroup status update", "err", err)
		return
	}
	if !podsHaveSlurmNodeAssignments(&slurmJobComponent.Pods, jobID) {
		return
	}

	updated := pg.DeepCopyObject().(*slurmjobir.PodGroup)
	now := metav1.Now()
	apimeta.SetStatusCondition(&updated.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: pg.GetGeneration(),
		LastTransitionTime: now,
		Reason:             "Scheduled",
		Message:            "Pod group admitted by " + sb.schedulerName,
	})
	if err := sb.Status().Patch(ctx, updated, client.StrategicMergeFrom(pg)); err != nil {
		logger.Error(err, "failed to patch PodGroup status", "podGroup", key)
		return
	}
	logger.V(4).Info("marked PodGroup scheduled", "podGroup", key)
}

func (sb *SlurmBridge) refreshSlurmJobIRPods(ctx context.Context, slurmJobIR *slurmjobir.SlurmJobIR) error {
	for i := range slurmJobIR.Components {
		for j := range slurmJobIR.Components[i].Pods.Items {
			key := client.ObjectKeyFromObject(&slurmJobIR.Components[i].Pods.Items[j])
			if err := sb.Get(ctx, key, &slurmJobIR.Components[i].Pods.Items[j]); err != nil {
				return err
			}
		}
	}
	return nil
}
