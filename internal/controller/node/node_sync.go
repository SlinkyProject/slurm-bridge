// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/util/taints"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/utils"
)

func (r *NodeReconciler) Sync(ctx context.Context, req reconcile.Request) error {
	var errs []error

	if err := r.syncTaint(ctx, req); err != nil {
		errs = append(errs, err)
	}

	if err := r.syncState(ctx, req); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// syncTaint will handle applying and removing the slurm-bridge taint on nodes.
// - If the k8s node overlaps with a slurm node, apply the taint.
// - Otherwise, remove the taint.
func (r *NodeReconciler) syncTaint(ctx context.Context, req reconcile.Request) error {
	logger := log.FromContext(ctx)

	node := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Get Slurm NodeNames
	slurmNodeNames, err := r.slurmControl.GetNodeNames(ctx)
	if err != nil {
		return err
	}
	slurmNodeNameSet := set.New(slurmNodeNames...)

	// Get Kubernetes Node Names for Slurm
	kubeNodeList := &corev1.NodeList{}
	if err := r.List(ctx, kubeNodeList); err != nil {
		return err
	}
	kubeNodeNameMap := nodeutils.MakeNodeNameMap(ctx, kubeNodeList)
	kubeNodeNameSet := set.New(utils.Keys(kubeNodeNameMap)...)

	bridgedNodeNames := slurmNodeNameSet.Intersection(kubeNodeNameSet)
	if bridgedNodeNames.Has(nodeutils.GetSlurmNodeName(node)) {
		// Requeue until no longer a bridged node
		durationStore.Push(node.Name, 30*time.Second)

		// Taint bridged Kubernetes nodes
		logger.V(1).Info("add taint to bridged node", "node", klog.KObj(node))
		return r.taintNode(ctx, node, kubeNodeNameMap)
	} else {
		// Untaint unbridged Kubernetes nodes
		logger.V(1).Info("remove taint from non-bridged node", "node", klog.KObj(node))
		return r.untaintNode(ctx, node, kubeNodeNameMap)
	}
}

func (r *NodeReconciler) taintNode(ctx context.Context, node *corev1.Node, nodeNameMap map[string]string) error {
	logger := log.FromContext(ctx)

	name, ok := nodeNameMap[nodeutils.GetSlurmNodeName(node)]
	if !ok {
		name = node.GetName()
	}

	// Fetch Node
	toUpdate := &corev1.Node{}
	key := types.NamespacedName{
		Name: name,
	}
	if err := r.Get(ctx, key, toUpdate); err != nil {
		logger.Error(err, "failed to get node", "node", klog.KObj(node))
		return err
	}

	// Add Node Taint
	toUpdate = toUpdate.DeepCopy()
	taint := utils.NewTaintNodeBridged(r.SchedulerName)
	toUpdate, _, err := taints.AddOrUpdateTaint(toUpdate, taint)
	if err != nil {
		logger.Error(err, "failed to add or update taint", "node", klog.KObj(node), "taint", taint)
		return err
	}
	patch := client.StrategicMergeFrom(node)
	if data, err := patch.Data(node); err != nil {
		logger.Error(err, "failed to unpack patch for node", "node", klog.KObj(node))
	} else if len(data) == 0 {
		logger.V(2).Info("node patch is empty, skipping patch request", "node", klog.KObj(node))
		return nil
	}
	logger.Info("Remove taint from node", "node", klog.KObj(node))
	if err := r.Patch(ctx, toUpdate, patch); err != nil {
		logger.Error(err, "failed to patch node", "node", klog.KObj(node))
		return err
	}
	return nil
}

func (r *NodeReconciler) untaintNode(ctx context.Context, node *corev1.Node, nodeNameMap map[string]string) error {
	logger := log.FromContext(ctx)

	name, ok := nodeNameMap[nodeutils.GetSlurmNodeName(node)]
	if !ok {
		name = node.GetName()
	}

	// Fetch Node
	toUpdate := &corev1.Node{}
	key := types.NamespacedName{
		Name: name,
	}
	if err := r.Get(ctx, key, toUpdate); err != nil {
		logger.Error(err, "failed to get node", "node", klog.KObj(node))
		return err
	}

	// Delete Node Taint
	toUpdate = toUpdate.DeepCopy()
	taint := utils.NewTaintNodeBridged(r.SchedulerName)
	toUpdate.Spec.Taints, _ = taints.DeleteTaint(toUpdate.Spec.Taints, taint)
	patch := client.StrategicMergeFrom(node)
	if data, err := patch.Data(node); err != nil {
		logger.Error(err, "failed to unpack patch for node", "node", klog.KObj(node))
	} else if len(data) == 0 {
		logger.V(2).Info("node patch is empty, skipping patch request", "node", klog.KObj(node))
		return nil
	}
	logger.Info("Add taint to node", "node", klog.KObj(node))
	if err := r.Patch(ctx, toUpdate, patch); err != nil {
		logger.Error(err, "failed to patch node", "node", klog.KObj(node))
		return err
	}
	return nil
}

// syncState will handle synchronizing Kubernetes node state and Slurm node state.
// Because Slurm is the source of scheduling truth, we only care about unidirectional
// propagation (e.g. Kubernetes => Slurm) and only states that inhibit scheduling in
// some way (e.g. Cordon, Drain).
func (r *NodeReconciler) syncState(ctx context.Context, req reconcile.Request) error {
	logger := log.FromContext(ctx)

	node := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// `kubectl [cordon|drain] $NODE` will make nodes unschedulable.
	if node.Spec.Unschedulable {
		reason := fmt.Sprintf("Corresponding Kubernetes node (%s) is unschedulable", klog.KObj(node))
		slurmNode := nodeutils.GetSlurmNodeName(node)
		logger.V(1).Info("draining Slurm node, Kubernetes node is unschedulable",
			"node", klog.KObj(node), "slurmNode", slurmNode, "reason", reason)
		if err := r.slurmControl.MakeNodeDrain(ctx, node, reason); err != nil {
			return err
		}
	} else {
		reason := fmt.Sprintf("Corresponding Kubernetes node (%s) is schedulable", klog.KObj(node))
		logger.V(1).Info("undraining Slurm node, Kubernetes node is schedulable",
			"node", klog.KObj(node))
		if err := r.slurmControl.MakeNodeUndrain(ctx, node, reason); err != nil {
			return err
		}
	}

	return nil
}
