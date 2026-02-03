// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmnode

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

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

	return nil
}
