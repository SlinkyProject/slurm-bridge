// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"

	resourcev1 "k8s.io/api/resource/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type inventoryReaderKey struct{}

// WithInventoryReader supplies the manager's synchronized inventory cache to
// plugins constructed by the embedded scheduler.
func WithInventoryReader(ctx context.Context, reader client.Reader) context.Context {
	return context.WithValue(ctx, inventoryReaderKey{}, reader)
}

// WithCachedInventory routes only inventory reads through the manager cache.
// Claims, Pods, writes and immediate readback retain the direct client's semantics.
func WithCachedInventory(ctx context.Context, direct client.Client) client.Client {
	reader, _ := ctx.Value(inventoryReaderKey{}).(client.Reader)
	if reader == nil {
		return direct
	}
	return &inventoryClient{Client: direct, inventory: reader}
}

type inventoryClient struct {
	client.Client
	inventory client.Reader
}

func (c *inventoryClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch obj.(type) {
	case *resourcev1.DeviceClass, *resourcev1.ResourceSlice:
		return c.inventory.Get(ctx, key, obj, opts...)
	default:
		return c.Client.Get(ctx, key, obj, opts...)
	}
}

func (c *inventoryClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch list.(type) {
	case *resourcev1.DeviceClassList, *resourcev1.ResourceSliceList:
		return c.inventory.List(ctx, list, opts...)
	default:
		return c.Client.List(ctx, list, opts...)
	}
}
