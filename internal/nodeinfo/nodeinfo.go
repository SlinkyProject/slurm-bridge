// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Represents a Kubernetes node for Slurm.
type NodeInfo struct {
	CpuMap CPUMap
	// CPUDevices holds the socket or NUMA node devices by name.
	CPUDevices map[string]resourcev1.Device
}

func NewNodeInfo(ctx context.Context, kubeclient client.Client, nodeName string) (*NodeInfo, error) {
	resourceSliceList := &resourcev1.ResourceSliceList{}
	if err := kubeclient.List(ctx, resourceSliceList); err != nil {
		return nil, err
	}
	return NewNodeInfoFromResourceSlices(nodeName, resourceSliceList.Items)
}

// NewNodeInfoFromResourceSlices builds CPU topology from an existing
// ResourceSlice snapshot.
func NewNodeInfoFromResourceSlices(nodeName string, resourceSlices []resourcev1.ResourceSlice) (*NodeInfo, error) {
	nodeInfo := &NodeInfo{CPUDevices: make(map[string]resourcev1.Device)}
	pool, cpuSlices, err := selectCPUResourcePool(nodeName, resourceSlices)
	if err != nil {
		return nil, err
	}

	var cpuInfos []*CPUInfo
	deviceSlices := make(map[string]string)
	cpuIDSlices := make(map[int]string)
	groups := make(map[cpuDeviceGroup]string)
	for _, resourceSlice := range cpuSlices {
		sliceCPUInfos, err := NewCPUInfos(resourceSlice)
		if err != nil {
			return nil, err
		}
		for _, device := range resourceSlice.Spec.Devices {
			if previous, ok := deviceSlices[device.Name]; ok {
				return nil, fmt.Errorf("duplicate device name %q in ResourceSlices %q and %q", device.Name, previous, resourceSlice.Name)
			}
			group := cpuDeviceGroupOf(device)
			if previous, ok := groups[group]; ok {
				return nil, fmt.Errorf("devices %q and %q both represent %s", previous, device.Name, group)
			}
			groups[group] = device.Name
			nodeInfo.CPUDevices[device.Name] = *device.DeepCopy()
			deviceSlices[device.Name] = resourceSlice.Name
		}
		for _, cpuInfo := range sliceCPUInfos {
			if previousSlice, found := cpuIDSlices[cpuInfo.CpuID]; found {
				return nil, fmt.Errorf("DRA CPU resource pool %q contains duplicate CPU ID %d in ResourceSlices %q and %q", pool, cpuInfo.CpuID, previousSlice, resourceSlice.Name)
			}
			cpuIDSlices[cpuInfo.CpuID] = resourceSlice.Name
		}
		cpuInfos = append(cpuInfos, sliceCPUInfos...)
	}
	if len(cpuInfos) != 0 {
		if err := validateCPUDeviceGroups(groups); err != nil {
			return nil, err
		}
		if err := validateCPUCoreTopology(pool, cpuInfos); err != nil {
			return nil, err
		}
		nodeInfo.CpuMap = NewCPUMap(pool, cpuInfos)
		if _, _, _, err := nodeInfo.Geometry(); err != nil {
			return nil, err
		}
		for _, device := range nodeInfo.CPUDevices {
			socket := cpuDeviceGroupOf(device).socket
			siblings, err := decodeSMTLayout(*device.Attributes[DraDriverCpu_SMTLayout].StringValue)
			if err != nil {
				return nil, err
			}
			for _, info := range cpuInfos {
				if info.SocketID != socket {
					continue
				}
				if other, ok := nodeInfo.CpuMap.CPUInfoMap[siblings[info.CpuID]]; ok && other.SocketID != socket {
					return nil, fmt.Errorf("CPU %d has a sibling on another socket", info.CpuID)
				}
			}
		}
	}

	return nodeInfo, nil
}

// cpuDeviceGroup identifies a grouped CPU device; numaNode is -1 for sockets.
type cpuDeviceGroup struct {
	socket   int
	numaNode int
}

func (g cpuDeviceGroup) String() string {
	if g.numaNode < 0 {
		return fmt.Sprintf("socket %d", g.socket)
	}
	return fmt.Sprintf("NUMA node %d", g.numaNode)
}

// cpuDeviceGroupOf returns the group of a device which NewCPUInfos accepted.
func cpuDeviceGroupOf(device resourcev1.Device) cpuDeviceGroup {
	group := cpuDeviceGroup{socket: int(*device.Attributes[DraDriverCpu_SocketID].IntValue), numaNode: -1}
	if numa, ok := device.Attributes[DraDriverCpu_NUMANodeID]; ok && numa.IntValue != nil {
		group.numaNode = int(*numa.IntValue)
	}
	return group
}

// validateCPUDeviceGroups rejects mixed grouping and NUMA nodes spanning sockets.
func validateCPUDeviceGroups(groups map[cpuDeviceGroup]string) error {
	byNUMA := make(map[int]int)
	numaGrouped := 0
	for group := range groups {
		if group.numaNode < 0 {
			continue
		}
		numaGrouped++
		if socket, ok := byNUMA[group.numaNode]; ok && socket != group.socket {
			return fmt.Errorf("NUMA node %d spans sockets %d and %d", group.numaNode, socket, group.socket)
		}
		byNUMA[group.numaNode] = group.socket
	}
	if numaGrouped != 0 && numaGrouped != len(groups) {
		return fmt.Errorf("CPU devices mix socket and NUMA node grouping")
	}
	return nil
}

// sortedCPUDeviceNames orders devices by socket, then NUMA node, which keeps
// the order of earlier socket-grouped releases.
func (n *NodeInfo) sortedCPUDeviceNames() []string {
	names := slices.Collect(maps.Keys(n.CPUDevices))
	slices.SortFunc(names, func(a, b string) int {
		ga, gb := cpuDeviceGroupOf(n.CPUDevices[a]), cpuDeviceGroupOf(n.CPUDevices[b])
		return cmp.Or(cmp.Compare(ga.socket, gb.socket), cmp.Compare(ga.numaNode, gb.numaNode), cmp.Compare(a, b))
	})
	return names
}

// NUMACores returns the Slurm cores of each NUMA node, or nil unless the CPU
// inventory is grouped by NUMA node. Slurm binds GRES only to whole sockets,
// so NUMA nodes sharing a socket are omitted.
func (n *NodeInfo) NUMACores() map[int][]int {
	if n == nil {
		return nil
	}
	numaPerSocket := make(map[int]int)
	for _, device := range n.CPUDevices {
		group := cpuDeviceGroupOf(device)
		if group.numaNode < 0 {
			return nil
		}
		numaPerSocket[group.socket]++
	}
	var cores map[int][]int
	for _, device := range n.CPUDevices {
		group := cpuDeviceGroupOf(device)
		// TODO: Register NUMA nodes as Slurm sockets for sub-NUMA clustering.
		if numaPerSocket[group.socket] != 1 {
			continue
		}
		cpus, err := parseCPUSet(ptr.Deref(device.Attributes[DraDriverCpu_CPUIDs].StringValue, ""))
		if err != nil {
			return nil
		}
		if cores == nil {
			cores = make(map[int][]int)
		}
		for _, cpu := range cpus.List() {
			core, ok := n.CpuMap.MachineToAbstract[cpu]
			if ok && !slices.Contains(cores[group.numaNode], core) {
				cores[group.numaNode] = append(cores[group.numaNode], core)
			}
		}
		slices.Sort(cores[group.numaNode])
	}
	return cores
}

type cpuCoreID struct {
	socket int
	core   int
}

func validateCPUCoreTopology(pool string, cpuInfos []*CPUInfo) error {
	coreTypes := make(map[cpuCoreID]CoreType)
	threadsPerCore := make(map[cpuCoreID]int)
	for _, cpuInfo := range cpuInfos {
		core := cpuCoreID{socket: cpuInfo.SocketID, core: cpuInfo.CoreID}
		if coreType, found := coreTypes[core]; found && coreType != cpuInfo.CoreType {
			return fmt.Errorf("DRA CPU resource pool %q has inconsistent core types for socket %d core %d", pool, core.socket, core.core)
		}
		coreTypes[core] = cpuInfo.CoreType
		if cpuInfo.CoreType != CoreTypeEfficiency {
			threadsPerCore[core]++
		}
	}

	wantThreads := 0
	for core, threads := range threadsPerCore {
		if wantThreads == 0 {
			wantThreads = threads
			continue
		}
		if threads != wantThreads {
			return fmt.Errorf("DRA CPU resource pool %q has non-uniform topology: socket %d core %d has %d threads, expected %d", pool, core.socket, core.core, threads, wantThreads)
		}
	}
	if wantThreads == 0 {
		return fmt.Errorf("DRA CPU resource pool %q contains no Slurm-compatible CPU devices", pool)
	}
	return nil
}

type cpuPoolSnapshot struct {
	generation         int64
	resourceSliceCount int64
	slices             []*resourcev1.ResourceSlice
}

func selectCPUResourcePool(nodeName string, resourceSlices []resourcev1.ResourceSlice) (string, []*resourcev1.ResourceSlice, error) {
	snapshotsByPool := make(map[string]*cpuPoolSnapshot)
	for i := range resourceSlices {
		resourceSlice := &resourceSlices[i]
		if resourceSlice.Spec.Driver == DraDriverCpu && resourceSlice.Spec.NodeName == nil && (resourceSlice.Spec.NodeSelector != nil || ptr.Deref(resourceSlice.Spec.AllNodes, false) || ptr.Deref(resourceSlice.Spec.PerDeviceNodeSelection, false)) {
			return "", nil, fmt.Errorf("CPU inventory requires a node-local ResourceSlice")
		}
		if resourceSlice.Spec.Driver != DraDriverCpu || ptr.Deref(resourceSlice.Spec.NodeName, "") != nodeName {
			continue
		}

		pool := resourceSlice.Spec.Pool
		if pool.Name == "" || pool.Generation < 0 || pool.ResourceSliceCount <= 0 {
			return "", nil, fmt.Errorf("invalid CPU ResourceSlice pool %q", pool.Name)
		}
		if ptr.Deref(resourceSlice.Spec.PerDeviceNodeSelection, false) {
			return "", nil, fmt.Errorf("CPU per-device node selection is unsupported")
		}
		snapshot, ok := snapshotsByPool[pool.Name]
		if !ok || pool.Generation > snapshot.generation {
			snapshotsByPool[pool.Name] = &cpuPoolSnapshot{
				generation:         pool.Generation,
				resourceSliceCount: pool.ResourceSliceCount,
				slices:             []*resourcev1.ResourceSlice{resourceSlice},
			}
			continue
		}
		if pool.Generation < snapshot.generation {
			continue
		}
		if pool.ResourceSliceCount != snapshot.resourceSliceCount {
			return "", nil, fmt.Errorf("DRA CPU resource pool %q generation %d has inconsistent resourceSliceCount values %d and %d", pool.Name, snapshot.generation, snapshot.resourceSliceCount, pool.ResourceSliceCount)
		}
		snapshot.slices = append(snapshot.slices, resourceSlice)
	}

	poolNames := make([]string, 0, len(snapshotsByPool))
	for pool := range snapshotsByPool {
		poolNames = append(poolNames, pool)
	}
	slices.SortFunc(poolNames, cmp.Compare)
	if len(poolNames) > 1 {
		return "", nil, fmt.Errorf("DRA CPU inventory for node %q spans multiple resource pools %q", nodeName, poolNames)
	}
	if len(poolNames) == 0 {
		return "", nil, nil
	}

	pool := poolNames[0]
	snapshot := snapshotsByPool[pool]
	if int64(len(snapshot.slices)) != snapshot.resourceSliceCount {
		return "", nil, fmt.Errorf("DRA CPU resource pool %q generation %d is incomplete: found %d of %d ResourceSlices", pool, snapshot.generation, len(snapshot.slices), snapshot.resourceSliceCount)
	}
	slices.SortFunc(snapshot.slices, func(a, b *resourcev1.ResourceSlice) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return pool, snapshot.slices, nil
}
