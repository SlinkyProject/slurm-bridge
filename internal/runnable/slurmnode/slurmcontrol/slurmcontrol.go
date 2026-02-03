// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"net/http"

	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/types"
	"k8s.io/utils/ptr"
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
		if tolerateError(err) {
			return nil
		}
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
