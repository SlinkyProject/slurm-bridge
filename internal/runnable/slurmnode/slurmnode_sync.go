// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmnode

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
)

func (r *SlurmJobRunnable) Sync(ctx context.Context) error {
	if err := r.slurmControl.RefreshNodeCache(ctx); err != nil {
		return err
	}

	slurmNodeNames, err := r.slurmControl.ListNodeNames(ctx)
	if err != nil {
		return err
	}

	kubeNodeList := &corev1.NodeList{}
	if err := r.List(ctx, kubeNodeList); err != nil {
		return err
	}
	kubeNodeNameMap := nodeutils.MakeNodeNameMap(ctx, kubeNodeList)

	for _, slurmNodename := range slurmNodeNames {
		kubeNodeName, ok := kubeNodeNameMap[slurmNodename]
		if !ok {
			continue
		}
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: kubeNodeName,
			},
		}
		r.eventCh <- event.GenericEvent{Object: node}
	}

	// An empty list is more likely a bad read than a cluster with no nodes; never remove on it.
	if len(kubeNodeList.Items) == 0 {
		return nil
	}
	return r.removeDeletedNodes(ctx, kubeNodeNameMap)
}

// removeDeletedNodes removes external Slurm nodes that slurm-bridge registered for Kubernetes
// nodes that no longer exist. Otherwise Slurm keeps them idle and allocates jobs to them.
func (r *SlurmJobRunnable) removeDeletedNodes(ctx context.Context, kubeNodeNameMap map[string]string) error {
	logger := log.FromContext(ctx)

	bridgeNodeNames, err := r.slurmControl.ListBridgeNodeNames(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range bridgeNodeNames {
		if _, ok := kubeNodeNameMap[name]; ok {
			continue
		}
		logger.Info("Removing Slurm node whose Kubernetes node was deleted", "slurmNode", name)
		if err := r.slurmControl.RemoveNode(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
