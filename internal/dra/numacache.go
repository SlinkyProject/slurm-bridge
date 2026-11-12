// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"sync"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/client-go/tools/cache"
)

// NUMASpanCache derives GRES ordering once per observed inventory change.
// Informer objects are immutable; only references and the derived spans are kept.
// The registry is fixed for the lifetime of the informer.
type NUMASpanCache struct {
	mu        sync.Mutex
	registry  *Registry
	slices    map[string]*resourcev1.ResourceSlice
	spans     map[string]float64
	dirty     bool
	hasSynced func() bool
}

type resourceSliceInformer interface {
	AddEventHandler(cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error)
}

func NewNUMASpanCache(registry *Registry, informer resourceSliceInformer) (*NUMASpanCache, error) {
	if registry == nil {
		registry = DefaultRegistry()
	}
	c := &NUMASpanCache{registry: registry, slices: make(map[string]*resourcev1.ResourceSlice), dirty: true}
	registration, err := informer.AddEventHandler(c)
	if err != nil {
		return nil, err
	}
	c.hasSynced = registration.HasSynced
	return c, nil
}

func (c *NUMASpanCache) OnAdd(obj any, _ bool) {
	slice := obj.(*resourcev1.ResourceSlice)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slices[slice.Name] != slice {
		c.slices[slice.Name] = slice
		c.dirty = true
	}
}

func (c *NUMASpanCache) OnUpdate(_, obj any) { c.OnAdd(obj, false) }

func (c *NUMASpanCache) OnDelete(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	slice, ok := obj.(*resourcev1.ResourceSlice)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.slices[slice.Name]; current != nil && current.UID == slice.UID {
		delete(c.slices, slice.Name)
		c.dirty = true
	}
}

// Spans returns a caller-owned subset. Before initial sync, use name ordering.
func (c *NUMASpanCache) Spans(ctx context.Context, profiles []string) map[string]float64 {
	if !c.hasSynced() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirty {
		slices := make([]*resourcev1.ResourceSlice, 0, len(c.slices))
		for _, slice := range c.slices {
			slices = append(slices, slice)
		}
		names := make([]string, 0, len(c.registry.byName))
		for name := range c.registry.byName {
			names = append(names, name)
		}
		spans := NUMASpans(ctx, c.registry, slices, names)
		if ctx.Err() != nil {
			return nil // Never retain a computation interrupted by a request deadline.
		}
		c.spans, c.dirty = spans, false
	}
	result := make(map[string]float64, len(profiles))
	for _, name := range profiles {
		if span, ok := c.spans[name]; ok {
			result[name] = span
		}
	}
	return result
}
