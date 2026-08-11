// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"

	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/types"
)

type SlurmControlInterface interface {
	// RefreshNodeCache forces the Node cache to be refreshed
	RefreshNodeCache(ctx context.Context) error
	// ListNodeNames returns a list of Slurm nodes
	ListNodeNames(ctx context.Context) ([]string, error)
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
}

// RefreshNodeCache implements SlurmControlInterface.
func (r *realSlurmControl) RefreshNodeCache(ctx context.Context) error {
	nodeList := &types.V0044NodeList{}
	opts := &client.ListOptions{
		RefreshCache: true,
	}
	if err := r.List(ctx, nodeList, opts); err != nil {
		return err
	}
	return nil
}

// ListNodeNames implements SlurmControlInterface.
func (r *realSlurmControl) ListNodeNames(ctx context.Context) ([]string, error) {
	list := &types.V0044NodeList{}
	if err := r.List(ctx, list); err != nil {
		return nil, err
	}
	nodenames := make([]string, len(list.Items))
	for i, node := range list.Items {
		nodenames[i] = ptr.Deref(node.Name, "")
	}
	return nodenames, nil
}

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(client client.Client) SlurmControlInterface {
	return &realSlurmControl{
		Client: client,
	}
}
