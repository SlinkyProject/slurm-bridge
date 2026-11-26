// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmnode

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func (r *SlurmJobRunnable) Sync(ctx context.Context) error {
	if err := r.slurmControl.RefreshNodeCache(ctx); err != nil {
		return err
	}

	slurmNodeNames, bridgeNodeNames, err := r.slurmControl.ListNodeNames(ctx)
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
	return r.removeDeletedNodes(ctx, bridgeNodeNames, kubeNodeNameMap)
}

// removeDeletedNodes removes external Slurm nodes that slurm-bridge registered for Kubernetes
// nodes that no longer exist. Otherwise Slurm keeps them idle and allocates jobs to them.
func (r *SlurmJobRunnable) removeDeletedNodes(ctx context.Context, bridgeNodeNames []string, kubeNodeNameMap map[string]string) error {
	logger := log.FromContext(ctx)

	var errs []error
	for _, name := range bridgeNodeNames {
		if _, ok := kubeNodeNameMap[name]; ok {
			continue
		}
		exists, err := r.kubeNodeExists(ctx, name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if exists {
			continue
		}
		logger.Info("Removing Slurm node whose Kubernetes node was deleted", "slurmNode", name)
		if err := r.slurmControl.RemoveNode(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// kubeNodeExists asks the API server directly, since a Node registered moments ago
// may not be in the cache yet.
func (r *SlurmJobRunnable) kubeNodeExists(ctx context.Context, slurmNodeName string) (bool, error) {
	err := r.apiReader.Get(ctx, client.ObjectKey{Name: slurmNodeName}, &corev1.Node{})
	if err == nil {
		return true, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	// A longer Node name can match by name, but cannot appear in the alias label.
	if len(content.IsLabelValue(slurmNodeName)) != 0 {
		return false, nil
	}
	list := &corev1.NodeList{}
	if err := r.apiReader.List(ctx, list, client.MatchingLabels{wellknown.LabelSlurmNodeName: slurmNodeName}, client.Limit(1)); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}
