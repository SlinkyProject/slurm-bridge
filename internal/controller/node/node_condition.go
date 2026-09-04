// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/node/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const (
	reasonSlurmGRESCompatible        = "SlurmGRESCompatible"
	reasonIncompatibleSlurmGRES      = "IncompatibleSlurmGRES"
	reasonSlurmGRESVerificationError = "SlurmGRESVerificationError"
)

func (r *NodeReconciler) setSlurmGRESCompatibilityCondition(
	ctx context.Context,
	node *corev1.Node,
	status corev1.ConditionStatus,
	reason string,
	message string,
) (bool, error) {
	condition := findNodeCondition(node.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
	transitioned := condition == nil || condition.Status != status
	if condition != nil && condition.Status == status && condition.Reason == reason && condition.Message == message {
		return false, nil
	}

	now := metav1.Now()
	updated := corev1.NodeCondition{
		Type:               wellknown.NodeConditionSlurmGRESCompatible,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastHeartbeatTime:  now,
		LastTransitionTime: now,
	}
	if condition != nil && condition.Status == status {
		updated.LastTransitionTime = condition.LastTransitionTime
	}
	// Conditions merge by type under a strategic merge patch, so the cached
	// node is a sufficient base: the patch only touches this condition.
	patched := node.DeepCopy()
	setNodeCondition(&patched.Status.Conditions, updated)
	if err := r.Status().Patch(ctx, patched, client.StrategicMergeFrom(node)); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("patching Slurm GRES compatibility condition on Kubernetes node %q: %w", node.Name, err)
	}
	return transitioned, nil
}

func (r *NodeReconciler) clearSlurmGRESCompatibilityCondition(ctx context.Context, node *corev1.Node) error {
	if findNodeCondition(node.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible) == nil {
		return nil
	}
	patched := node.DeepCopy()
	patched.Status.Conditions = slices.DeleteFunc(patched.Status.Conditions, func(c corev1.NodeCondition) bool {
		return c.Type == wellknown.NodeConditionSlurmGRESCompatible
	})
	if err := r.Status().Patch(ctx, patched, client.StrategicMergeFrom(node)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("clearing Slurm GRES compatibility condition on Kubernetes node %q: %w", node.Name, err)
	}
	return nil
}

func (r *NodeReconciler) recordSlurmGRESCompatibilityError(ctx context.Context, node *corev1.Node, err error) error {
	var incompatibleGRES *slurmcontrol.IncompatibleGRESConfigurationError
	if errors.As(err, &incompatibleGRES) {
		transitioned, conditionErr := r.setSlurmGRESCompatibilityCondition(
			ctx,
			node,
			corev1.ConditionFalse,
			reasonIncompatibleSlurmGRES,
			err.Error(),
		)
		if transitioned && conditionErr == nil && r.eventRecorder != nil {
			r.eventRecorder.Event(node, corev1.EventTypeWarning, reasonIncompatibleSlurmGRES, err.Error())
		}
		return errors.Join(err, conditionErr)
	}

	_, conditionErr := r.setSlurmGRESCompatibilityCondition(
		ctx,
		node,
		corev1.ConditionUnknown,
		reasonSlurmGRESVerificationError,
		fmt.Sprintf("Could not verify Slurm GRES compatibility: %v", err),
	)
	return errors.Join(err, conditionErr)
}

func (r *NodeReconciler) recordIncompatibleSlurmGRESError(ctx context.Context, node *corev1.Node, err error) error {
	var incompatibleGRES *slurmcontrol.IncompatibleGRESConfigurationError
	if !errors.As(err, &incompatibleGRES) {
		return err
	}
	return r.recordSlurmGRESCompatibilityError(ctx, node, err)
}

func findNodeCondition(conditions []corev1.NodeCondition, conditionType corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func setNodeCondition(conditions *[]corev1.NodeCondition, condition corev1.NodeCondition) {
	for i := range *conditions {
		if (*conditions)[i].Type == condition.Type {
			(*conditions)[i] = condition
			return
		}
	}
	*conditions = append(*conditions, condition)
}
