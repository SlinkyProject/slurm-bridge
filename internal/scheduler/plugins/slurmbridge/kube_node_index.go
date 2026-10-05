// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
)

// kubeNodeNameIndex uses the scheduler's existing Node watch. Lookups require
// initial synchronization and accept the informer's temporary staleness.
type kubeNodeNameIndex struct {
	index     cache.Indexer
	hasSynced cache.InformerSynced
}

func newKubeNodeNameIndex(informer cache.SharedIndexInformer) (*kubeNodeNameIndex, error) {
	if _, exists := informer.GetIndexer().GetIndexers()[nodeutils.IndexFieldSlurmNodeName]; !exists {
		if err := informer.AddIndexers(cache.Indexers{
			nodeutils.IndexFieldSlurmNodeName: func(obj any) ([]string, error) {
				node, ok := obj.(*corev1.Node)
				if !ok {
					return nil, fmt.Errorf("expected Kubernetes Node, got %T", obj)
				}
				return nodeutils.IndexNodeBySlurmName(node), nil
			},
		}); err != nil {
			return nil, fmt.Errorf("index Kubernetes nodes by Slurm name: %w", err)
		}
	}
	return &kubeNodeNameIndex{index: informer.GetIndexer(), hasSynced: informer.HasSynced}, nil
}

func (c *kubeNodeNameIndex) lookup(ctx context.Context, slurmNodes []string) (sets.Set[string], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.hasSynced() {
		return nil, fmt.Errorf("kubernetes node informer is not synced")
	}
	names := sets.New[string]()
	for _, name := range slurmNodes {
		nodes, err := c.index.ByIndex(nodeutils.IndexFieldSlurmNodeName, name)
		if err != nil {
			return nil, err
		}
		if len(nodes) > 1 {
			return nil, fmt.Errorf("slurm node %q maps to multiple Kubernetes nodes", name)
		}
		if len(nodes) == 1 {
			names.Insert(nodes[0].(*corev1.Node).Name)
			continue
		}
		// Preserve the existing fallback to an exact Kubernetes name, using
		// the same informer store instead of an API request.
		_, exists, err := c.index.GetByKey(name)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrorNoKubeNodeMatch, name)
		}
		names.Insert(name)
	}
	return names, nil
}

func (sb *SlurmBridge) slurmToKubeNodes(ctx context.Context, slurmNodes []string) (sets.Set[string], error) {
	return sb.kubeNodeIndex.lookup(ctx, slurmNodes)
}
