// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmobject "github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type SlurmControlInterface interface {
	// GetNodeNames returns the list Slurm nodes by name.
	GetNodeNames(ctx context.Context) ([]string, error)
	// NodeExists returns true if the Slurm node exists, false if not found.
	NodeExists(ctx context.Context, node *corev1.Node) (bool, error)
	// MakeNodeDrain handles adding the DRAIN state to the Slurm node.
	MakeNodeDrain(ctx context.Context, node *corev1.Node, reason string) error
	// MakeNodeUndrain handles removing the DRAIN state from the Slurm node.
	MakeNodeUndrain(ctx context.Context, node *corev1.Node, reason string) error
	// IsNodeDrain checks if the slurm node has the DRAIN state.
	IsNodeDrain(ctx context.Context, node *corev1.Node) (bool, error)
	// IsNodeDrained checks if the slurm node is DRAINED and eligible for removal.
	IsNodeDrained(ctx context.Context, node *corev1.Node) (bool, error)
	// AddNode registers a Kubernetes node in Slurm with the correct CPUs and memory.
	AddNode(ctx context.Context, node *corev1.Node, nodeInfo *nodeinfo.NodeInfo) error
	// RemoveNode removes a Kubernetes node from Slurm.
	RemoveNode(ctx context.Context, node *corev1.Node) error
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	slurmclient.Client
}

// GetNodeNames implements SlurmControlInterface.
func (r *realSlurmControl) GetNodeNames(ctx context.Context) ([]string, error) {
	list := &slurmtypes.V0044NodeList{}
	if err := r.List(ctx, list); err != nil {
		return nil, err
	}
	nodenames := make([]string, len(list.Items))
	for i, node := range list.Items {
		nodenames[i] = *node.Name
	}
	return nodenames, nil
}

// NodeExists implements SlurmControlInterface.
func (r *realSlurmControl) NodeExists(ctx context.Context, node *corev1.Node) (bool, error) {
	key := slurmobject.ObjectKey(nodeutils.GetSlurmNodeName(node))
	slurmNode := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, key, slurmNode); err != nil {
		if tolerateError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

const nodeReasonPrefix = "slurm-bridge:"

// MakeNodeDrain implements SlurmControlInterface.
func (r *realSlurmControl) MakeNodeDrain(ctx context.Context, node *corev1.Node, reason string) error {
	logger := log.FromContext(ctx)

	slurmNode := &slurmtypes.V0044Node{}
	key := slurmobject.ObjectKey(nodeutils.GetSlurmNodeName(node))
	if err := r.Get(ctx, key, slurmNode); err != nil {
		if tolerateError(err) {
			return nil
		}
		return err
	}

	if slurmNode.GetStateAsSet().Has(api.V0044NodeStateDRAIN) {
		logger.V(1).Info("node is already drained, skipping drain request",
			"node", slurmNode.GetKey(), "nodeState", slurmNode.State)
		return nil
	}

	logger.Info("Make Slurm node drain", "node", klog.KObj(node))
	req := api.V0044UpdateNodeMsg{
		State:  ptr.To([]api.V0044UpdateNodeMsgState{api.V0044UpdateNodeMsgStateDRAIN}),
		Reason: ptr.To(nodeReasonPrefix + " " + reason),
	}
	if err := r.Update(ctx, slurmNode, req); err != nil {
		if tolerateError(err) {
			return nil
		}
		return err
	}

	return nil
}

// MakeNodeUndrain implements SlurmControlInterface.
func (r *realSlurmControl) MakeNodeUndrain(ctx context.Context, node *corev1.Node, reason string) error {
	logger := log.FromContext(ctx)

	slurmNode := &slurmtypes.V0044Node{}
	key := slurmobject.ObjectKey(nodeutils.GetSlurmNodeName(node))
	opts := &slurmclient.GetOptions{RefreshCache: true}
	if err := r.Get(ctx, key, slurmNode, opts); err != nil {
		if tolerateError(err) {
			return nil
		}
		return err
	}

	nodeReason := ptr.Deref(slurmNode.Reason, "")
	if !slurmNode.GetStateAsSet().Has(api.V0044NodeStateDRAIN) ||
		slurmNode.GetStateAsSet().Has(api.V0044NodeStateUNDRAIN) {
		logger.V(1).Info("Node is already undrained, skipping undrain request",
			"node", slurmNode.GetKey(), "nodeState", slurmNode.State)
		return nil
	} else if nodeReason != "" && !strings.Contains(nodeReason, nodeReasonPrefix) {
		logger.Info("Node was drained but not by slurm-bridge, skipping undrain request",
			"node", slurmNode.GetKey(), "nodeReason", nodeReason)
		return nil
	}

	logger.Info("Make Slurm node undrain", "node", klog.KObj(node))
	req := api.V0044UpdateNodeMsg{
		State:  ptr.To([]api.V0044UpdateNodeMsgState{api.V0044UpdateNodeMsgStateUNDRAIN}),
		Reason: ptr.To(nodeReasonPrefix + " " + reason),
	}
	if err := r.Update(ctx, slurmNode, req); err != nil {
		if tolerateError(err) {
			return nil
		}
		return err
	}

	return nil
}

// IsNodeDrain implements SlurmControlInterface.
func (r *realSlurmControl) IsNodeDrain(ctx context.Context, node *corev1.Node) (bool, error) {
	key := slurmobject.ObjectKey(nodeutils.GetSlurmNodeName(node))
	slurmNode := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, key, slurmNode); err != nil {
		return false, err
	}

	isDrain := slurmNode.GetStateAsSet().Has(api.V0044NodeStateDRAIN)
	return isDrain, nil
}

// IsNodeDrained implements SlurmControlInterface.
func (r *realSlurmControl) IsNodeDrained(ctx context.Context, node *corev1.Node) (bool, error) {
	key := slurmobject.ObjectKey(nodeutils.GetSlurmNodeName(node))
	slurmNode := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, key, slurmNode, &slurmclient.GetOptions{RefreshCache: true}); err != nil {
		return false, err
	}

	state := slurmNode.GetStateAsSet()
	isDrain := state.Has(api.V0044NodeStateDRAIN) && !state.Has(api.V0044NodeStateUNDRAIN)
	isBusy := state.HasAny(api.V0044NodeStateALLOCATED, api.V0044NodeStateMIXED, api.V0044NodeStateCOMPLETING)
	return isDrain && !isBusy, nil
}

// AddNode implements SlurmControlInterface.
func (r *realSlurmControl) AddNode(ctx context.Context, node *corev1.Node, nodeInfo *nodeinfo.NodeInfo) error {
	logger := log.FromContext(ctx)

	slurmNodeName := nodeutils.GetSlurmNodeName(node)
	key := slurmobject.ObjectKey(slurmNodeName)
	slurmNode := &slurmtypes.V0044Node{}
	err := r.Get(ctx, key, slurmNode, &slurmclient.GetOptions{SkipCache: true})
	if err == nil {
		return r.updateNodeFeatures(ctx, node, slurmNode)
	}
	if !tolerateError(err) {
		return err
	}

	cpus := node.Status.Capacity.Cpu().Value()
	memoryBytes := node.Status.Capacity.Memory().Value()
	memoryMB := memoryBytes / (1024 * 1024)

	gres, gresConf := "", ""
	if nodeInfo != nil {
		gres, gresConf = nodeInfo.GetGresAndGresConf()
	}

	annotations := node.GetAnnotations()
	features := ""
	if partitionsAnno, ok := annotations[wellknown.AnnotationExternalNodePartitions]; ok && partitionsAnno != "" {
		partitions := splitPartitionList(partitionsAnno)
		for _, partition := range partitions {
			if err := r.validatePartitionExists(ctx, partition); err != nil {
				return fmt.Errorf("could not validate partition %q: %w", partition, err)
			}
		}
		features = strings.Join(partitions, ",")
	}

	// Create node configuration string
	// Format: NodeName=<name> CPUs=<cpus> RealMemory=<memory_mb> State=External [Feature=<features>] [Gres=<gres>] [GresConf=<gresconf>]
	nodeConf := fmt.Sprintf("NodeName=%s CPUs=%d RealMemory=%d State=External",
		slurmNodeName, cpus, memoryMB)
	if features != "" {
		nodeConf += fmt.Sprintf(" Feature=%s", features)
	}
	if gres != "" {
		nodeConf += fmt.Sprintf(" Gres=%s", gres)
	}
	if gresConf != "" {
		nodeConf += fmt.Sprintf(" GresConf=%s", gresConf)
	}

	logger.Info("Adding Kubernetes node to Slurm",
		"node", klog.KObj(node),
		"slurmNode", slurmNodeName,
		"cpus", cpus,
		"memoryMB", memoryMB,
		"features", features,
		"gres", gres,
		"gresConf", gresConf)

	req := api.V0044OpenapiCreateNodeReq{
		NodeConf: nodeConf,
	}
	if err := r.Create(ctx, slurmNode, req); err != nil {
		logger.Error(err, "Failed to add node to Slurm", "node", klog.KObj(node),
			"slurmNode", slurmNodeName)
		return err
	}

	return nil
}

// updateNodeFeatures updates an existing Slurm node so its features match the
// partitions annotation.
func (r *realSlurmControl) updateNodeFeatures(ctx context.Context, node *corev1.Node, slurmNode *slurmtypes.V0044Node) error {
	logger := log.FromContext(ctx)

	annotations := node.GetAnnotations()
	partitionsAnno, ok := annotations[wellknown.AnnotationExternalNodePartitions]
	if !ok || partitionsAnno == "" {
		return nil
	}
	partitions := splitPartitionList(partitionsAnno)
	for _, partition := range partitions {
		if err := r.validatePartitionExists(ctx, partition); err != nil {
			return fmt.Errorf("could not validate partition %q: %w", partition, err)
		}
	}
	if featuresEqual(slurmNode.Features, partitions) && featuresEqual(slurmNode.Partitions, partitions) {
		return nil
	}
	partitionsCsv := api.V0044CsvString(partitions)
	req := api.V0044UpdateNodeMsg{
		Features:    ptr.To(partitionsCsv),
		FeaturesAct: ptr.To(partitionsCsv),
	}
	logger.Info("Updating Slurm node features to match annotation", "node", klog.KObj(node),
		"slurmNode", slurmNode.GetKey(), "features", partitions)
	if err := r.Update(ctx, slurmNode, req); err != nil {
		return fmt.Errorf("could not update node features: %w", err)
	}

	// Request reconfigure so Slurm repopulates partition membership from node features
	// This is necessary due to a bug in Slurm where node feature updates do not update
	// partition membership. Once the issue is fixed, this will be removed.
	reconfigureObj := &slurmtypes.V0044Reconfigure{}
	if err := r.Get(ctx, reconfigureObj.GetKey(), reconfigureObj); err != nil {
		return fmt.Errorf("could not request Slurm reconfigure: %w", err)
	}

	return nil
}

// RemoveNode implements SlurmControlInterface.
func (r *realSlurmControl) RemoveNode(ctx context.Context, node *corev1.Node) error {
	logger := log.FromContext(ctx)

	slurmNodeName := nodeutils.GetSlurmNodeName(node)

	key := slurmobject.ObjectKey(slurmNodeName)
	slurmNode := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, key, slurmNode, &slurmclient.GetOptions{SkipCache: true}); err != nil {
		if tolerateError(err) {
			return nil
		}
		return err
	}

	logger.Info("Removing Kubernetes node from Slurm", "node", klog.KObj(node),
		"slurmNode", slurmNodeName)
	if err := r.Delete(ctx, slurmNode); err != nil {
		if tolerateError(err) {
			return nil
		}
		return fmt.Errorf("could not remove node from Slurm: %w", err)
	}

	return nil
}

// validatePartitionExists checks if a Slurm partition exists using the GetPartitionInfo API.
func (r *realSlurmControl) validatePartitionExists(ctx context.Context, partitionName string) error {
	partition := &slurmtypes.V0044PartitionInfo{}
	key := slurmobject.ObjectKey(partitionName)
	if err := r.Get(ctx, key, partition); err != nil {
		if tolerateError(err) {
			return fmt.Errorf("partition not found")
		}
		return err
	}
	return nil
}

// featuresEqual reports whether the Slurm node's features (nil or *[]string) match the
// desired partition list. Comparison is order-independent.
func featuresEqual(current *api.V0044CsvString, desired []string) bool {
	var cur []string
	if current != nil {
		cur = *current
	}
	if len(cur) != len(desired) {
		return false
	}
	curSorted := make([]string, len(cur))
	copy(curSorted, cur)
	desSorted := make([]string, len(desired))
	copy(desSorted, desired)
	sort.Strings(curSorted)
	sort.Strings(desSorted)
	for i := range curSorted {
		if curSorted[i] != desSorted[i] {
			return false
		}
	}
	return true
}

// splitPartitionList splits a comma-separated annotation value into partition names (trimmed, non-empty).
func splitPartitionList(value string) []string {
	if value == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(value, ",") {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(client slurmclient.Client) SlurmControlInterface {
	return &realSlurmControl{
		Client: client,
	}
}

func tolerateError(err error) bool {
	if err == nil {
		return true
	}
	errText := err.Error()
	notFound := http.StatusText(http.StatusNotFound)
	noContent := http.StatusText(http.StatusNoContent)
	if strings.Contains(errText, notFound) || strings.Contains(errText, noContent) {
		return true
	}
	return false
}
