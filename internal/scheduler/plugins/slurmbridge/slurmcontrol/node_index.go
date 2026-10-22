// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/object"
)

const nodePartitionIndex = "slurmbridge-node-partition"

func nodePartitionKeys(obj object.Object) []string {
	return ptr.Deref(obj.(*api.V0044Node).Partitions, nil)
}

// GetNodeNames reads the node informer's partition index. ByIndex waits for
// synchronization and propagates refresh failures; it never falls back to a
// live node list on the scheduling path.
func (r *realSlurmControl) GetNodeNames(ctx context.Context, partition *string) ([]string, error) {
	names := sets.New[string]()
	for _, name := range strings.Split(ptr.Deref(partition, r.partition), ",") {
		nodes := &api.V0044NodeObjectList{}
		if err := r.nodes.ByIndex(ctx, nodePartitionIndex, strings.TrimSpace(name), nodes); err != nil {
			return nil, err
		}
		for _, node := range nodes.Items {
			if name := ptr.Deref(node.Name, ""); name != "" {
				names.Insert(name)
			}
		}
	}
	return sets.List(names), nil
}
