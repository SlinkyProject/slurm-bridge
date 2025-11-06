// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v0044 "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmobject "github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
)

type SlurmControlInterface interface {
	// GetNodeNames returns the list Slurm nodes by name.
	GetNodeNames(ctx context.Context) ([]string, error)
	// MakeNodeDrain handles adding the DRAIN state to the Slurm node.
	MakeNodeDrain(ctx context.Context, node *corev1.Node, reason string) error
	// MakeNodeUndrain handles removing the DRAIN state from the Slurm node.
	MakeNodeUndrain(ctx context.Context, node *corev1.Node, reason string) error
	// IsNodeDrain checks if the slurm node has the DRAIN state.
	IsNodeDrain(ctx context.Context, node *corev1.Node) (bool, error)
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

	if slurmNode.GetStateAsSet().Has(v0044.V0044NodeStateDRAIN) {
		logger.V(1).Info("node is already drained, skipping drain request",
			"node", slurmNode.GetKey(), "nodeState", slurmNode.State)
		return nil
	}

	logger.Info("Make Slurm node drain", "node", klog.KObj(node))
	req := v0044.V0044UpdateNodeMsg{
		State:  ptr.To([]v0044.V0044UpdateNodeMsgState{v0044.V0044UpdateNodeMsgStateDRAIN}),
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
	if !slurmNode.GetStateAsSet().Has(v0044.V0044NodeStateDRAIN) ||
		slurmNode.GetStateAsSet().Has(v0044.V0044NodeStateUNDRAIN) {
		logger.V(1).Info("Node is already undrained, skipping undrain request",
			"node", slurmNode.GetKey(), "nodeState", slurmNode.State)
		return nil
	} else if nodeReason != "" && !strings.Contains(nodeReason, nodeReasonPrefix) {
		logger.Info("Node was drained but not by slurm-bridge, skipping undrain request",
			"node", slurmNode.GetKey(), "nodeReason", nodeReason)
		return nil
	}

	logger.Info("Make Slurm node undrain", "node", klog.KObj(node))
	req := v0044.V0044UpdateNodeMsg{
		State:  ptr.To([]v0044.V0044UpdateNodeMsgState{v0044.V0044UpdateNodeMsgStateUNDRAIN}),
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

	isDrain := slurmNode.GetStateAsSet().Has(v0044.V0044NodeStateDRAIN)
	return isDrain, nil
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
	if errText == http.StatusText(http.StatusNotFound) ||
		errText == http.StatusText(http.StatusNoContent) {
		return true
	}
	return false
}
