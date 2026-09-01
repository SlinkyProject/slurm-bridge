// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/utils/ptr"
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

// IndexFieldResourceSliceNode is the field index name under which ResourceSlices are
// indexed by the node they apply to: the exact node name for the common per-node case
// (Spec.NodeName set), or IndexValueResourceSliceGlobal for slices that can apply to more
// than one node (NodeSelector, AllNodes, or PerDeviceNodeSelection), so a node's relevant
// ResourceSlices can be resolved without listing every ResourceSlice in the cluster.
const IndexFieldResourceSliceNode = "slurmBridge.resourceSliceNode"

// IndexValueResourceSliceGlobal is the index value used for ResourceSlices that are not
// scoped to a single node name.
const IndexValueResourceSliceGlobal = "*"

// IndexResourceSliceByNode is the IndexerFunc for IndexFieldResourceSliceNode.
func IndexResourceSliceByNode(obj client.Object) []string {
	resourceSlice, ok := obj.(*resourcev1.ResourceSlice)
	if !ok {
		return nil
	}
	if nodeName := ptr.Deref(resourceSlice.Spec.NodeName, ""); nodeName != "" && !ptr.Deref(resourceSlice.Spec.PerDeviceNodeSelection, false) {
		return []string{nodeName}
	}
	return []string{IndexValueResourceSliceGlobal}
}

// SetupFieldIndexers registers the field indexes used by the node controller and the
// slurmnode runnable to resolve a Kubernetes Node from a Slurm node name, and to resolve
// the ResourceSlices relevant to a given Node.
func SetupFieldIndexers(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Node{}, IndexFieldSlurmNodeName, IndexNodeBySlurmName); err != nil {
		return err
	}
	return mgr.GetFieldIndexer().IndexField(context.Background(), &resourcev1.ResourceSlice{}, IndexFieldResourceSliceNode, IndexResourceSliceByNode)
}

// GetResourceSlicesForNode returns the ResourceSlices relevant to nodeName: those scoped
// to it by name, plus any that can apply to more than one node (NodeSelector, AllNodes, or
// PerDeviceNodeSelection), using IndexFieldResourceSliceNode instead of listing every
// ResourceSlice in the cluster. Callers must still evaluate node-selector matching
// themselves for the latter group.
//
// If reader doesn't support the index, this falls back to listing every ResourceSlice, so
// callers stay correct regardless of which client they were constructed with.
func GetResourceSlicesForNode(ctx context.Context, reader client.Reader, nodeName string) ([]resourcev1.ResourceSlice, error) {
	perNode := &resourcev1.ResourceSliceList{}
	err := reader.List(ctx, perNode, client.MatchingFields{IndexFieldResourceSliceNode: nodeName})
	if err != nil {
		all := &resourcev1.ResourceSliceList{}
		if err := reader.List(ctx, all); err != nil {
			return nil, err
		}
		return all.Items, nil
	}
	global := &resourcev1.ResourceSliceList{}
	if err := reader.List(ctx, global, client.MatchingFields{IndexFieldResourceSliceNode: IndexValueResourceSliceGlobal}); err != nil {
		return nil, err
	}
	return append(perNode.Items, global.Items...), nil
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
