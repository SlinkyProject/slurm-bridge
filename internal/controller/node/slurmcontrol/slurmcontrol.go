// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	slurmobject "github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
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
	// IsNodeExternal checks if the slurm node is an external node
	IsNodeExternal(ctx context.Context, node *corev1.Node) (bool, error)
	// AddNode registers an external Kubernetes node in Slurm, or reconciles
	// bridge-owned metadata when the Slurm node already exists.
	AddNode(ctx context.Context, node *corev1.Node, nodeInfo *nodeinfo.NodeInfo, draInventory []dra.GRESInventory) error
	// UpdateHybridNode reconciles the bridge-owned Extra inventory on an
	// existing hybrid node. It never creates an absent node or modifies an
	// external node.
	UpdateHybridNode(ctx context.Context, node *corev1.Node, draInventory []dra.GRESInventory) error
	// NodeNeedsRecreate returns true when an external Slurm node's CPU, memory,
	// or GRES configuration must be applied by draining and recreating it. For
	// hybrid nodes, it validates that the static GRES configuration can
	// represent the DRA inventory.
	NodeNeedsRecreate(ctx context.Context, node *corev1.Node, nodeInfo *nodeinfo.NodeInfo, draInventory []dra.GRESInventory) (bool, error)
	// RemoveNode removes a Kubernetes node from Slurm.
	RemoveNode(ctx context.Context, node *corev1.Node) error
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	slurmclient.Client
}

// IncompatibleGRESConfigurationError reports that an existing hybrid Slurm
// node cannot represent the DRA inventory discovered on its Kubernetes node.
// The error includes the minimum gres.conf inventory needed to fix the node.
type IncompatibleGRESConfigurationError struct {
	nodeName string
	current  string
	required string
	gresConf []string
}

func (e *IncompatibleGRESConfigurationError) Error() string {
	return fmt.Sprintf(
		"slurm node %q has GRES %q, which is incompatible with required DRA GRES %q; gres.conf needs equivalent inventory entries (include the File, AutoDetect, and Flags settings appropriate for the hardware):\n%s",
		e.nodeName,
		e.current,
		e.required,
		strings.Join(e.gresConf, "\n"),
	)
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
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

// IsNodeExternal implements SlurmControlInterface.
func (r *realSlurmControl) IsNodeExternal(ctx context.Context, node *corev1.Node) (bool, error) {
	key := slurmobject.ObjectKey(nodeutils.GetSlurmNodeName(node))
	slurmNode := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, key, slurmNode); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return false, nil
		}
		return false, err
	}

	isNodeExternal := slurmNode.GetStateAsSet().Has(api.V0044NodeStateEXTERNAL)
	return isNodeExternal, nil
}

// NodeNeedsRecreate implements SlurmControlInterface.
func (r *realSlurmControl) NodeNeedsRecreate(ctx context.Context, node *corev1.Node, nodeInfo *nodeinfo.NodeInfo, draInventory []dra.GRESInventory) (bool, error) {
	key := slurmobject.ObjectKey(nodeutils.GetSlurmNodeName(node))
	slurmNode := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, key, slurmNode); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return false, nil
		}
		return false, err
	}

	desiredCPU := desiredNodeCPUConfig(node, nodeInfo)
	desiredMemoryMB := node.Status.Capacity.Memory().Value() / (1024 * 1024)
	desiredGRES, err := buildNodeGRESConfig(draInventory)
	if err != nil {
		return false, err
	}

	currentMemoryMB := ptr.Deref(slurmNode.RealMemory, int64(0))
	currentGres := ptr.Deref(slurmNode.Gres, "")
	currentExtra := ptr.Deref(slurmNode.Extra, "")
	isExternal := slurmNode.GetStateAsSet().Has(api.V0044NodeStateEXTERNAL)
	if desiredGRES.extra != "" && currentExtra != "" && !strings.HasPrefix(currentExtra, dra.AppliedInventoryExtraPrefix) {
		return false, fmt.Errorf("cannot record applied DRA inventory on Slurm node %q: Extra field is already in use", key)
	}
	extraChanged := desiredGRES.extra != currentExtra &&
		(desiredGRES.extra != "" || strings.HasPrefix(currentExtra, dra.AppliedInventoryExtraPrefix))
	gresChanged := desiredGRES.gres != currentGres
	if !isExternal {
		if err := validateHybridNodeGRES(string(key), currentGres, desiredGRES); err != nil {
			return false, err
		}
		// Hybrid nodes are registered by slurmd. Their static GRES configuration
		// is validated above, and their bridge-owned Extra value is patched in
		// AddNode instead of draining and recreating the node.
		gresChanged = false
		extraChanged = false
	}

	cpuChanged := desiredCPU.cpus != int(ptr.Deref(slurmNode.Cpus, 0))
	if desiredCPU.fromDRA {
		cpuChanged = cpuChanged ||
			desiredCPU.sockets != int(ptr.Deref(slurmNode.Sockets, 0)) ||
			desiredCPU.coresPerSocket != int(ptr.Deref(slurmNode.Cores, 0)) ||
			desiredCPU.threadsPerCore != int(ptr.Deref(slurmNode.Threads, 0))
	}

	if cpuChanged || desiredMemoryMB != currentMemoryMB || gresChanged || extraChanged {
		return true, nil
	}
	return false, nil
}

// AddNode implements SlurmControlInterface.
func (r *realSlurmControl) AddNode(ctx context.Context, node *corev1.Node, nodeInfo *nodeinfo.NodeInfo, draInventory []dra.GRESInventory) error {
	logger := log.FromContext(ctx)

	slurmNodeName := nodeutils.GetSlurmNodeName(node)
	key := slurmobject.ObjectKey(slurmNodeName)
	gresConfig, err := buildNodeGRESConfig(draInventory)
	if err != nil {
		return err
	}

	slurmNode := &slurmtypes.V0044Node{}
	err = r.Get(ctx, key, slurmNode, &slurmclient.GetOptions{SkipCache: true})
	if err == nil {
		return r.updateExistingNode(ctx, node, slurmNode, gresConfig)
	}
	if err != nil && !errors.Is(err, slurmerrors.ErrNotFound) {
		return err
	}

	cpuConfig := desiredNodeCPUConfig(node, nodeInfo)
	memoryBytes := node.Status.Capacity.Memory().Value()
	memoryMB := memoryBytes / (1024 * 1024)

	annotations := node.GetAnnotations()
	features := []string{wellknown.SlurmFeatureGRESCompatible}
	if partitionsAnno, ok := annotations[wellknown.AnnotationExternalNodePartitions]; ok && partitionsAnno != "" {
		partitions := splitPartitionList(partitionsAnno)
		for _, partition := range partitions {
			if err := r.validatePartitionExists(ctx, partition); err != nil {
				return fmt.Errorf("could not validate partition %q: %w", partition, err)
			}
		}
		features = append(features, partitions...)
	}

	// Create node configuration string
	// Format: NodeName=<name> CPUs=<cpus> RealMemory=<memory_mb> State=External [Feature=<features>] [Gres=<gres>] [GresConf=<gresconf>]
	nodeConf := fmt.Sprintf("NodeName=%s Sockets=1 CoresPerSocket=%d ThreadsPerCore=%d CPUs=%d RealMemory=%d State=External",
		slurmNodeName, cpuConfig.coresPerSocket, cpuConfig.threadsPerCore, cpuConfig.cpus, memoryMB)
	nodeConf += fmt.Sprintf(" Feature=%s", strings.Join(features, ","))
	if topologySpec, ok := annotations[wellknown.AnnotationNodeTopologySpec]; ok && topologySpec != "" {
		nodeConf += fmt.Sprintf(" Topology=%s", topologySpec)
	}
	nodeConf += fmt.Sprintf(" Gres=\"%s\"", gresConfig.gres)
	nodeConf += fmt.Sprintf(" GresConf=\"%s\"", gresConfig.gresConf)

	logger.Info("Adding Kubernetes node to Slurm",
		"node", klog.KObj(node),
		"slurmNode", slurmNodeName,
		"cpus", cpuConfig.cpus,
		"memoryMB", memoryMB,
		"features", features,
		"topology", annotations[wellknown.AnnotationNodeTopologySpec],
		"gres", gresConfig.gres,
		"gresConf", gresConfig.gresConf)

	req := api.V0044OpenapiCreateNodeReq{
		NodeConf: nodeConf,
	}
	if err := r.Create(ctx, slurmNode, req); err != nil {
		logger.Error(err, "Failed to add node to Slurm", "node", klog.KObj(node),
			"slurmNode", slurmNodeName)
		return err
	}
	if gresConfig.extra != "" {
		createdNode := &slurmtypes.V0044Node{V0044Node: api.V0044Node{Name: ptr.To(slurmNodeName)}}
		req := api.V0044UpdateNodeMsg{Extra: ptr.To(gresConfig.extra)}
		if err := r.Update(ctx, createdNode, req); err != nil {
			return fmt.Errorf("could not record applied DRA inventory on Slurm node %q: %w", slurmNodeName, err)
		}
	}

	return nil
}

// UpdateHybridNode implements SlurmControlInterface.
func (r *realSlurmControl) UpdateHybridNode(ctx context.Context, node *corev1.Node, draInventory []dra.GRESInventory) error {
	slurmNodeName := nodeutils.GetSlurmNodeName(node)
	key := slurmobject.ObjectKey(slurmNodeName)
	gresConfig, err := buildNodeGRESConfig(draInventory)
	if err != nil {
		return err
	}

	slurmNode := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, key, slurmNode, &slurmclient.GetOptions{SkipCache: true}); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return nil
		}
		return err
	}
	if slurmNode.GetStateAsSet().Has(api.V0044NodeStateEXTERNAL) {
		return nil
	}
	return r.reconcileHybridNode(ctx, slurmNode, gresConfig)
}

func (r *realSlurmControl) reconcileHybridNode(
	ctx context.Context,
	slurmNode *slurmtypes.V0044Node,
	gresConfig nodeGRESConfig,
) error {
	if err := validateHybridNodeGRES(string(slurmNode.GetKey()), ptr.Deref(slurmNode.Gres, ""), gresConfig); err != nil {
		return errors.Join(err, r.updateGRESCompatibilityFeature(ctx, slurmNode, false))
	}
	if err := r.updateNodeExtra(ctx, slurmNode, gresConfig.extra); err != nil {
		return errors.Join(err, r.updateGRESCompatibilityFeature(ctx, slurmNode, false))
	}
	return r.updateGRESCompatibilityFeature(ctx, slurmNode, true)
}

type nodeCPUConfig struct {
	sockets        int
	coresPerSocket int
	threadsPerCore int
	cpus           int
	fromDRA        bool
}

func desiredNodeCPUConfig(node *corev1.Node, nodeInfo *nodeinfo.NodeInfo) nodeCPUConfig {
	cpus := int(node.Status.Capacity.Cpu().Value())
	cores := cpus
	fromDRA := nodeInfo != nil && len(nodeInfo.CpuMap.AbstractToMachine) > 0
	if fromDRA {
		cpus = len(nodeInfo.CpuMap.MachineToAbstract)
		cores = len(nodeInfo.CpuMap.AbstractToMachine)
	}

	threadsPerCore := 1
	if cores > 0 {
		threadsPerCore = cpus / cores
	}

	return nodeCPUConfig{
		sockets:        1,
		coresPerSocket: cores,
		threadsPerCore: threadsPerCore,
		cpus:           cpus,
		fromDRA:        fromDRA,
	}
}

type nodeGRESConfig struct {
	gres              string
	gresConf          string
	extra             string
	gresConfInventory []string
}

func buildNodeGRESConfig(draInventory []dra.GRESInventory) (nodeGRESConfig, error) {
	var gresEntries, gresConfEntries, gresConfInventory []string
	for _, inventory := range draInventory {
		gres, gresConf, err := inventory.SlurmConfig()
		if err != nil {
			return nodeGRESConfig{}, err
		}
		gresEntries = append(gresEntries, gres)
		gresConfEntries = append(gresConfEntries, gresConf)
		gresConfInventory = append(gresConfInventory, fmt.Sprintf(
			"Name=%s Type=%s Count=%d",
			inventory.GRES.Name,
			inventory.GRES.Type,
			len(inventory.Devices),
		))
	}

	config := nodeGRESConfig{
		gres:              strings.Join(gresEntries, ","),
		gresConf:          strings.Join(gresConfEntries, "+"),
		gresConfInventory: gresConfInventory,
	}
	if len(draInventory) > 0 {
		extra, err := dra.EncodeAppliedInventory(draInventory)
		if err != nil {
			return nodeGRESConfig{}, err
		}
		config.extra = extra
	}
	return config, nil
}

func (r *realSlurmControl) updateExistingNode(
	ctx context.Context,
	node *corev1.Node,
	slurmNode *slurmtypes.V0044Node,
	gresConfig nodeGRESConfig,
) error {
	if !slurmNode.GetStateAsSet().Has(api.V0044NodeStateEXTERNAL) {
		if err := r.reconcileHybridNode(ctx, slurmNode, gresConfig); err != nil {
			return err
		}
		return r.updateNodeTopology(ctx, node, slurmNode)
	}
	if err := r.updateNodeFeatures(ctx, node, slurmNode); err != nil {
		return err
	}
	return r.updateNodeTopology(ctx, node, slurmNode)
}

// updateGRESCompatibilityFeature adds or removes only the feature owned by
// slurm-bridge, preserving all administrator-managed node features. Slurm
// requires active features to be removed before their available feature.
func (r *realSlurmControl) updateGRESCompatibilityFeature(
	ctx context.Context,
	slurmNode *slurmtypes.V0044Node,
	compatible bool,
) error {
	available := updateFeatureList(
		ptr.Deref(slurmNode.Features, api.V0044CsvString{}),
		wellknown.SlurmFeatureGRESCompatible,
		compatible,
	)
	active := updateFeatureList(
		ptr.Deref(slurmNode.ActiveFeatures, api.V0044CsvString{}),
		wellknown.SlurmFeatureGRESCompatible,
		compatible,
	)
	availableChanged := !featuresEqual(slurmNode.Features, available)
	activeChanged := !featuresEqual(slurmNode.ActiveFeatures, active)
	if !availableChanged && !activeChanged {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("Updating Slurm node GRES compatibility feature",
		"slurmNode", slurmNode.GetKey(),
		"compatible", compatible)

	updateAvailable := func() error {
		if !availableChanged {
			return nil
		}
		features := api.V0044CsvString(available)
		if err := r.Update(ctx, slurmNode, api.V0044UpdateNodeMsg{Features: ptr.To(features)}); err != nil {
			return fmt.Errorf("could not update node available GRES compatibility feature: %w", err)
		}
		return nil
	}
	updateActive := func() error {
		if !activeChanged {
			return nil
		}
		features := api.V0044CsvString(active)
		if err := r.Update(ctx, slurmNode, api.V0044UpdateNodeMsg{FeaturesAct: ptr.To(features)}); err != nil {
			return fmt.Errorf("could not update node active GRES compatibility feature: %w", err)
		}
		return nil
	}

	if compatible {
		if err := updateAvailable(); err != nil {
			return err
		}
		return updateActive()
	}
	if err := updateActive(); err != nil {
		return err
	}
	return updateAvailable()
}

func updateFeatureList(current []string, feature string, present bool) []string {
	updated := make([]string, 0, len(current)+1)
	for _, currentFeature := range current {
		if currentFeature != feature {
			updated = append(updated, currentFeature)
		}
	}
	if present {
		updated = append(updated, feature)
	}
	return updated
}

// validateHybridNodeGRES checks that every DRA-managed GRES entry is present
// exactly as configured on an existing slurmd-registered node. Other GRES
// entries are intentionally ignored so administrators can expose additional
// resources which are not managed by slurm-bridge.
func validateHybridNodeGRES(nodeName, current string, desired nodeGRESConfig) error {
	if desired.gres == "" {
		return nil
	}

	currentEntries := make(map[string]struct{})
	for entry := range strings.SplitSeq(current, ",") {
		entry = strings.TrimSpace(entry)
		if suffix := strings.IndexByte(entry, '('); suffix >= 0 {
			entry = entry[:suffix]
		}
		currentEntries[entry] = struct{}{}
	}
	for required := range strings.SplitSeq(desired.gres, ",") {
		if _, ok := currentEntries[required]; !ok {
			gresConf := make([]string, len(desired.gresConfInventory))
			for i, entry := range desired.gresConfInventory {
				gresConf[i] = fmt.Sprintf("NodeName=%s %s", nodeName, entry)
			}
			return &IncompatibleGRESConfigurationError{
				nodeName: nodeName,
				current:  current,
				required: desired.gres,
				gresConf: gresConf,
			}
		}
	}
	return nil
}

// updateNodeExtra reconciles only the Extra values owned by slurm-bridge. An
// unrelated non-empty value is preserved unless DRA inventory needs the field,
// in which case callers receive an actionable error rather than data loss.
func (r *realSlurmControl) updateNodeExtra(ctx context.Context, slurmNode *slurmtypes.V0044Node, desired string) error {
	current := ptr.Deref(slurmNode.Extra, "")
	if current == desired || desired == "" && !strings.HasPrefix(current, dra.AppliedInventoryExtraPrefix) {
		return nil
	}
	if current != "" && !strings.HasPrefix(current, dra.AppliedInventoryExtraPrefix) {
		return fmt.Errorf("cannot record applied DRA inventory on Slurm node %q: Extra field is already in use", slurmNode.GetKey())
	}

	logger := log.FromContext(ctx)
	logger.Info("Updating Slurm node applied DRA inventory", "node", slurmNode.GetKey())
	req := api.V0044UpdateNodeMsg{Extra: ptr.To(desired)}
	if err := r.Update(ctx, slurmNode, req); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("could not record applied DRA inventory on Slurm node %q: %w", slurmNode.GetKey(), err)
	}
	return nil
}

// updateNodeTopology updates an existing Slurm node so its dynamic topology
// matches the Kubernetes node topology annotation.
func (r *realSlurmControl) updateNodeTopology(ctx context.Context, node *corev1.Node, slurmNode *slurmtypes.V0044Node) error {
	logger := log.FromContext(ctx)

	topologySpec := node.GetAnnotations()[wellknown.AnnotationNodeTopologySpec]
	if ptr.Deref(slurmNode.Topology, "") == topologySpec {
		return nil
	}

	req := api.V0044UpdateNodeMsg{
		TopologyStr: ptr.To(topologySpec),
	}
	logger.Info("Updating Slurm node topology to match annotation", "node", klog.KObj(node),
		"slurmNode", slurmNode.GetKey(), "topology", topologySpec)
	if err := r.Update(ctx, slurmNode, req); err != nil {
		return fmt.Errorf("could not update node topology: %w", err)
	}

	return nil
}

// updateNodeFeatures updates an existing external Slurm node so its features
// include GRES compatibility and, when configured, match the partitions
// annotation.
func (r *realSlurmControl) updateNodeFeatures(ctx context.Context, node *corev1.Node, slurmNode *slurmtypes.V0044Node) error {
	logger := log.FromContext(ctx)

	annotations := node.GetAnnotations()
	partitionsAnno, hasPartitions := annotations[wellknown.AnnotationExternalNodePartitions]
	partitions := splitPartitionList(partitionsAnno)
	if hasPartitions && partitionsAnno != "" {
		for _, partition := range partitions {
			if err := r.validatePartitionExists(ctx, partition); err != nil {
				return fmt.Errorf("could not validate partition %q: %w", partition, err)
			}
		}
	}

	features := ptr.Deref(slurmNode.Features, api.V0044CsvString{})
	activeFeatures := ptr.Deref(slurmNode.ActiveFeatures, api.V0044CsvString{})
	if hasPartitions && partitionsAnno != "" {
		features = partitions
		activeFeatures = partitions
	}
	features = updateFeatureList(features, wellknown.SlurmFeatureGRESCompatible, true)
	activeFeatures = updateFeatureList(activeFeatures, wellknown.SlurmFeatureGRESCompatible, true)
	partitionsMatch := !hasPartitions || partitionsAnno == "" || featuresEqual(slurmNode.Partitions, partitions)
	if featuresEqual(slurmNode.Features, features) &&
		featuresEqual(slurmNode.ActiveFeatures, activeFeatures) &&
		partitionsMatch {
		return nil
	}
	featuresCsv := api.V0044CsvString(features)
	activeFeaturesCsv := api.V0044CsvString(activeFeatures)
	req := api.V0044UpdateNodeMsg{
		Features:    ptr.To(featuresCsv),
		FeaturesAct: ptr.To(activeFeaturesCsv),
	}
	logger.Info("Updating Slurm external node features", "node", klog.KObj(node),
		"slurmNode", slurmNode.GetKey(), "features", features)
	if err := r.Update(ctx, slurmNode, req); err != nil {
		return fmt.Errorf("could not update node features: %w", err)
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return nil
		}
		return err
	}

	logger.Info("Removing Kubernetes node from Slurm", "node", klog.KObj(node),
		"slurmNode", slurmNodeName)
	if err := r.Delete(ctx, slurmNode); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
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
		if errors.Is(err, slurmerrors.ErrNotFound) {
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
