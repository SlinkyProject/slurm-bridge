// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"

	"k8s.io/utils/ptr"

	slurmapi "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	slurmobject "github.com/SlinkyProject/slurm-client/pkg/object"
	slurmutils "github.com/SlinkyProject/slurm-client/pkg/utils"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type SlurmControlInterface interface {
	// RefreshNodeCache forces the Node cache to be refreshed
	RefreshNodeCache(ctx context.Context) error
	// ListNodeNames returns all Slurm nodes, and the external ones that slurm-bridge registered
	ListNodeNames(ctx context.Context) (all, bridge []string, err error)
	// RemoveNode drains a Slurm node, then deletes it
	RemoveNode(ctx context.Context, name string) error
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
}

// RefreshNodeCache implements SlurmControlInterface.
func (r *realSlurmControl) RefreshNodeCache(ctx context.Context) error {
	nodeList := &slurmapi.V0044NodeObjectList{}
	opts := &client.ListOptions{
		RefreshCache: true,
	}
	if err := r.List(ctx, nodeList, opts); err != nil {
		return err
	}
	return nil
}

// ListNodeNames implements SlurmControlInterface.
func (r *realSlurmControl) ListNodeNames(ctx context.Context) (all, bridge []string, err error) {
	list := &slurmapi.V0044NodeObjectList{}
	if err := r.List(ctx, list); err != nil {
		return nil, nil, err
	}
	all = make([]string, len(list.Items))
	for i, node := range list.Items {
		all[i] = ptr.Deref(node.Name, "")
		external := slurmutils.GetStateAsSet(node.State).Has(slurmapi.V0044NodeStateEXTERNAL)
		if external && ptr.Deref(node.Comment, "") == wellknown.SlurmNodeComment {
			bridge = append(bridge, all[i])
		}
	}
	return all, bridge, nil
}

func (r *realSlurmControl) RemoveNode(ctx context.Context, name string) error {
	node := &slurmapi.V0044Node{}
	if err := r.Get(ctx, slurmobject.ObjectKey(name), node); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return nil
		}
		return err
	}
	// Drain first so Slurm stops allocating the node even if the delete fails while a job holds it.
	if !slurmutils.GetStateAsSet(node.State).Has(slurmapi.V0044NodeStateDRAIN) {
		req := slurmapi.V0044UpdateNodeMsg{
			State:  ptr.To([]slurmapi.V0044UpdateNodeMsgState{slurmapi.V0044UpdateNodeMsgStateDRAIN}),
			Reason: ptr.To("slurm-bridge: Kubernetes node was deleted"),
		}
		// Each read-back waits for a full node cache refresh; the next Sync refreshes it anyway.
		if err := r.Update(ctx, node, req, &client.UpdateOptions{SkipReadAfterUpdate: true}); err != nil &&
			!errors.Is(err, slurmerrors.ErrNotFound) {
			return err
		}
	}
	if err := r.Delete(ctx, node, &client.DeleteOptions{SkipReadAfterDelete: true}); err != nil &&
		!errors.Is(err, slurmerrors.ErrNotFound) {
		return err
	}
	return nil
}

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(client client.Client) SlurmControlInterface {
	return &realSlurmControl{
		Client: client,
	}
}
