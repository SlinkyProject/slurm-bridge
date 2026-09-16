// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
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
// indexed by their single node name. Unsupported node selection is indexed under
// IndexValueResourceSliceGlobal so inventory validation can reject it.
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
	if dra.ValidateResourceSliceNode(resourceSlice) == nil {
		return []string{*resourceSlice.Spec.NodeName}
	}
	return []string{IndexValueResourceSliceGlobal}
}

// IndexFieldResourceSlicePool indexes ResourceSlices by driver and pool, across
// all nodes and generations, so node filtering cannot hide inconsistent pools.
const IndexFieldResourceSlicePool = "slurmBridge.resourceSlicePool"

// IndexResourceSliceByPool is the IndexerFunc for IndexFieldResourceSlicePool.
func IndexResourceSliceByPool(obj client.Object) []string {
	resourceSlice, ok := obj.(*resourcev1.ResourceSlice)
	if !ok {
		return nil
	}
	return []string{resourceSlice.Spec.Driver + "/" + resourceSlice.Spec.Pool.Name}
}

// SetupFieldIndexers registers the field indexes used by the node controller and the
// slurmnode runnable to resolve a Kubernetes Node from a Slurm node name, and to resolve
// the ResourceSlices relevant to a given Node.
func SetupFieldIndexers(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Node{}, IndexFieldSlurmNodeName, IndexNodeBySlurmName); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &resourcev1.ResourceSlice{}, IndexFieldResourceSliceNode, IndexResourceSliceByNode); err != nil {
		return err
	}
	return mgr.GetFieldIndexer().IndexField(context.Background(), &resourcev1.ResourceSlice{}, IndexFieldResourceSlicePool, IndexResourceSliceByPool)
}

// GetResourceSlicesForNode returns every slice in pools with a slice assigned to
// nodeName, plus pools using unsupported node selection. Include all generations
// and nodes so callers can validate the latest generation before filtering by node.
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
	seenPools := make(map[string]struct{})
	var resourceSlices []resourcev1.ResourceSlice
	for _, resourceSlice := range append(perNode.Items, global.Items...) {
		key := resourceSlice.Spec.Driver + "/" + resourceSlice.Spec.Pool.Name
		if _, seen := seenPools[key]; seen {
			continue
		}
		seenPools[key] = struct{}{}
		poolSlices, err := GetResourceSlicesForPool(ctx, reader, resourceSlice.Spec.Driver, resourceSlice.Spec.Pool.Name)
		if err != nil {
			return nil, err
		}
		resourceSlices = append(resourceSlices, poolSlices...)
	}
	return resourceSlices, nil
}

// GetResourceSlicesForPool returns all generations and node assignments for one
// driver/pool. Readers without the pool index fall back to a full list scan.
func GetResourceSlicesForPool(ctx context.Context, reader client.Reader, driver, pool string) ([]resourcev1.ResourceSlice, error) {
	resourceSlices := &resourcev1.ResourceSliceList{}
	if err := reader.List(ctx, resourceSlices, client.MatchingFields{IndexFieldResourceSlicePool: driver + "/" + pool}); err == nil {
		return resourceSlices.Items, nil
	}
	if err := reader.List(ctx, resourceSlices); err != nil {
		return nil, err
	}
	var poolSlices []resourcev1.ResourceSlice
	for _, resourceSlice := range resourceSlices.Items {
		if resourceSlice.Spec.Driver == driver && resourceSlice.Spec.Pool.Name == pool {
			poolSlices = append(poolSlices, resourceSlice)
		}
	}
	return poolSlices, nil
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
