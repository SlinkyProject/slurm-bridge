// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"context"
	"fmt"
	"sync"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

// CPUTopologyCache derives immutable CPU topology once per node inventory change.
// It retains only CPU informer references and one derived snapshot per queried
// node. Allocation results and DeviceClass decisions are never cached.
type CPUTopologyCache struct {
	mu        sync.Mutex
	slices    map[string]*resourcev1.ResourceSlice
	byNode    map[string]map[string]*resourcev1.ResourceSlice
	entries   map[string]*cpuTopologyEntry
	hasSynced func() bool
}

type cpuTopologyEntry struct {
	once   sync.Once
	slices []resourcev1.ResourceSlice
	info   *NodeInfo
	err    error
}

type cpuSliceInformer interface {
	AddEventHandler(cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error)
}

func NewCPUTopologyCache(informer cpuSliceInformer) (*CPUTopologyCache, error) {
	c := &CPUTopologyCache{
		slices:  make(map[string]*resourcev1.ResourceSlice),
		byNode:  make(map[string]map[string]*resourcev1.ResourceSlice),
		entries: make(map[string]*cpuTopologyEntry),
	}
	registration, err := informer.AddEventHandler(c)
	if err != nil {
		return nil, err
	}
	c.hasSynced = registration.HasSynced
	return c, nil
}

// removeLocked also invalidates the old node when a slice moves or changes driver.
func (c *CPUTopologyCache) removeLocked(s *resourcev1.ResourceSlice) {
	node := ptr.Deref(s.Spec.NodeName, "")
	delete(c.slices, s.Name)
	delete(c.byNode[node], s.Name)
	if len(c.byNode[node]) == 0 {
		delete(c.byNode, node)
	}
	c.invalidateLocked(node)
}

func (c *CPUTopologyCache) invalidateLocked(node string) {
	if node == "" {
		// Unsupported global CPU selection must invalidate every node so the
		// normal topology validation rejects it, rather than using old inventory.
		clear(c.entries)
	} else {
		delete(c.entries, node)
	}
}

func (c *CPUTopologyCache) OnAdd(obj any, _ bool) {
	s := obj.(*resourcev1.ResourceSlice)
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.slices[s.Name]; old != nil {
		if old == s {
			return
		}
		c.removeLocked(old)
	}
	if s.Spec.Driver != DraDriverCpu {
		return
	}
	node := ptr.Deref(s.Spec.NodeName, "")
	if c.byNode[node] == nil {
		c.byNode[node] = make(map[string]*resourcev1.ResourceSlice)
	}
	c.slices[s.Name], c.byNode[node][s.Name] = s, s
	c.invalidateLocked(node)
}

func (c *CPUTopologyCache) OnUpdate(_, obj any) { c.OnAdd(obj, false) }

func (c *CPUTopologyCache) OnDelete(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	s, ok := obj.(*resourcev1.ResourceSlice)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.slices[s.Name]; old != nil && old.UID == s.UID {
		c.removeLocked(old)
	}
}

// topology returns a private immutable snapshot. It must not escape this package.
// Building outside the mutex lets unrelated nodes initialize concurrently.
func (c *CPUTopologyCache) topology(node string) (*NodeInfo, error) {
	if !c.hasSynced() {
		return nil, fmt.Errorf("CPU inventory cache is not synchronized")
	}
	c.mu.Lock()
	entry := c.entries[node]
	if entry == nil {
		entry = &cpuTopologyEntry{}
		for _, s := range c.byNode[node] {
			entry.slices = append(entry.slices, *s)
		}
		if node != "" {
			for _, s := range c.byNode[""] {
				entry.slices = append(entry.slices, *s)
			}
		}
		c.entries[node] = entry
	}
	c.mu.Unlock()
	entry.once.Do(func() {
		entry.info, entry.err = NewNodeInfoFromResourceSlices(node, entry.slices)
		entry.slices = nil
	})
	return entry.info, entry.err
}

func (c *CPUTopologyCache) AllocateCPUs(ctx context.Context, reader client.Client, node string, resources *slurmcontrol.NodeResources, className, allocationKey string) (*CPUAllocation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resources == nil {
		return nil, fmt.Errorf("missing Slurm CPU allocation")
	}
	n, err := c.topology(node)
	if err != nil {
		return nil, err
	}
	if err := n.ValidateCPUFingerprint(resources.NodeExtra); err != nil {
		return nil, err
	}
	return n.AllocateCPUs(ctx, reader, resources, className, allocationKey)
}
