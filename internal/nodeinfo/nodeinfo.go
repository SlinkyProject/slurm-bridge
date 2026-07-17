// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

// Represents a Kubernetes node for Slurm.
type NodeInfo struct {
	CpuMap CPUMap
}

func (n *NodeInfo) GetCPUDeviceRequests(ctx context.Context, kubeclient client.Client, resources *slurmcontrol.NodeResources, deviceClassName string, requestedCount int64) ([]resourcev1.DeviceRequest, error) {
	var requests []resourcev1.DeviceRequest

	if resources == nil {
		return requests, nil
	}

	exists, err := deviceClassExists(ctx, kubeclient, deviceClassName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("core-bitmap resource requested but DeviceClass %q was not found", deviceClassName)
	}
	if resources.CoreBitmap != "" {
		cpuIDs, err := n.allocatedCPUDeviceIDs(resources.CoreBitmap, requestedCount)
		if err != nil {
			return nil, err
		}
		if len(cpuIDs) > 0 {
			cpuSetString := strings.ReplaceAll(fmt.Sprint(cpuIDs), " ", ",")
			requests = append(requests, resourcev1.DeviceRequest{
				Name: corev1.ResourceCPU.String(),
				Exactly: &resourcev1.ExactDeviceRequest{
					DeviceClassName: deviceClassName,
					AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
					Count:           requestedCount,
					Selectors: []resourcev1.DeviceSelector{
						{
							CEL: &resourcev1.CELDeviceSelector{
								Expression: fmt.Sprintf("device.attributes['%s'].cpuID in %s", DraDriverCpu, cpuSetString),
							},
						},
					},
				},
			})
		}
	}

	return requests, nil
}

func (n *NodeInfo) GetCPUDeviceRequestAllocationResults(ctx context.Context, kubeclient client.Client, resources *slurmcontrol.NodeResources, deviceClassName string, requestedCount int64) ([]resourcev1.DeviceRequestAllocationResult, error) {
	var devices []resourcev1.DeviceRequestAllocationResult

	if resources == nil {
		return devices, nil
	}

	exists, err := deviceClassExists(ctx, kubeclient, deviceClassName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("core-bitmap resource requested but DeviceClass %q was not found", deviceClassName)
	}
	if resources.CoreBitmap != "" {
		cpuIDs, err := n.allocatedCPUDeviceIDs(resources.CoreBitmap, requestedCount)
		if err != nil {
			return nil, err
		}
		for _, cpuID := range cpuIDs {
			cpuInfo, ok := n.CpuMap.CPUInfoMap[cpuID]
			if !ok {
				return nil, fmt.Errorf("cpu ID %d from Slurm allocation not found on node", cpuID)
			}
			devices = append(devices, resourcev1.DeviceRequestAllocationResult{
				Request: corev1.ResourceCPU.String(),
				Driver:  DraDriverCpu,
				Pool:    n.CpuMap.Pool,
				Device:  cpuInfo.Name,
			})
		}
	}

	return devices, nil
}

func (n *NodeInfo) allocatedCPUDeviceIDs(coreBitmap string, requestedCount int64) ([]int, error) {
	if requestedCount <= 0 {
		return nil, fmt.Errorf("requested CPU device count must be greater than zero, got %d", requestedCount)
	}
	bitmap, err := bitmaputil.NewFrom(coreBitmap)
	if err != nil {
		return nil, err
	}
	cpuIDs := n.CpuMap.ToMachineCPUs(bitmap).List()
	if requestedCount > int64(len(cpuIDs)) {
		return nil, fmt.Errorf("not enough CPUs in Slurm allocation: requested %d, allocated %d", requestedCount, len(cpuIDs))
	}
	return cpuIDs[:requestedCount], nil
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
	nodeInfo := &NodeInfo{}
	pool, cpuSlices, err := selectCPUResourcePool(nodeName, resourceSlices)
	if err != nil {
		return nil, err
	}

	var cpuInfos []*CPUInfo
	for _, resourceSlice := range cpuSlices {
		cpuInfos = append(cpuInfos, NewCPUInfos(resourceSlice)...)
	}
	if len(cpuInfos) != 0 {
		nodeInfo.CpuMap = NewCPUMap(pool, cpuInfos)
	}

	return nodeInfo, nil
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
		if resourceSlice.Spec.Driver != DraDriverCpu || ptr.Deref(resourceSlice.Spec.NodeName, "") != nodeName {
			continue
		}

		pool := resourceSlice.Spec.Pool
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

func deviceClassExists(ctx context.Context, kubeclient client.Client, deviceClassName string) (bool, error) {
	if deviceClassName == "" {
		return false, nil
	}
	deviceClass := &resourcev1.DeviceClass{}
	err := kubeclient.Get(ctx, types.NamespacedName{Name: deviceClassName}, deviceClass)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get DeviceClass %q: %w", deviceClassName, err)
	}
	return true, nil
}
