// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IndexFieldSlurmNodeName is the field index name under which Nodes are indexed by their
// Slurm node name (see GetSlurmNodeName), so the corresponding Kubernetes Node can be
// resolved without listing every Node in the cluster.
const IndexFieldSlurmNodeName = "slurmBridge.slurmNodeName"

// IndexNodeBySlurmName is the IndexerFunc for IndexFieldSlurmNodeName.
func IndexNodeBySlurmName(obj client.Object) []string {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}
	return []string{GetSlurmNodeName(node)}
}

// SetupFieldIndexers registers the field indexes used by the node controller and the
// slurmnode runnable to resolve a Kubernetes Node from a Slurm node name.
func SetupFieldIndexers(mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Node{}, IndexFieldSlurmNodeName, IndexNodeBySlurmName)
}

// GetNodeNameForSlurmName resolves the Kubernetes node name for a given Slurm node name,
// using IndexFieldSlurmNodeName instead of listing every Node. If multiple Nodes share the
// same Slurm node name, the last one returned by the index is preferred, mirroring the
// last-write-wins behavior of the old MakeNodeNameMap-based lookup.
//
// If reader doesn't support the index (e.g. a client that talks directly to the API server
// instead of a manager's indexed cache), this falls back to listing every Node, so callers
// stay correct regardless of which client they were constructed with.
func GetNodeNameForSlurmName(ctx context.Context, reader client.Reader, slurmName string) (string, bool, error) {
	nodeList := &corev1.NodeList{}
	if err := reader.List(ctx, nodeList, client.MatchingFields{IndexFieldSlurmNodeName: slurmName}); err != nil {
		nodeList = &corev1.NodeList{}
		if err := reader.List(ctx, nodeList); err != nil {
			return "", false, err
		}
		name, ok := MakeNodeNameMap(ctx, nodeList)[slurmName]
		return name, ok, nil
	}
	if len(nodeList.Items) == 0 {
		return "", false, nil
	}
	return nodeList.Items[len(nodeList.Items)-1].GetName(), true, nil
}
