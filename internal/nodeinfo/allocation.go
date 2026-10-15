// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

// CPUAllocation holds claim spec and status data from one verified topology snapshot.
// CPUCount counts Linux CPUs; each result represents one socket share.
type CPUAllocation struct {
	Requests []resourcev1.DeviceRequest
	Results  []resourcev1.DeviceRequestAllocationResult
	Config   []resourcev1.DeviceClaimConfiguration
	CPUCount int64
}

func (n *NodeInfo) AllocateCPUs(ctx context.Context, kubeclient client.Client, resources *slurmcontrol.NodeResources, className, allocationKey string) (*CPUAllocation, error) {
	if resources == nil || resources.CoreBitmap == "" {
		return nil, fmt.Errorf("missing Slurm CPU allocation bitmap")
	}
	if allocationKey == "" {
		return nil, fmt.Errorf("missing CPU allocation identity")
	}
	class := &resourcev1.DeviceClass{}
	if err := kubeclient.Get(ctx, types.NamespacedName{Name: className}, class); err != nil {
		return nil, fmt.Errorf("get CPU DeviceClass %q: %w", className, err)
	}
	if len(class.Spec.Config) > 0 {
		return nil, fmt.Errorf("CPU DeviceClass configuration is unsupported")
	}
	sockets, cores, _, err := n.Geometry()
	if err != nil {
		return nil, err
	}
	if sockets == 0 {
		return nil, fmt.Errorf("no applied socket CPU inventory")
	}
	if (resources.SocketsPerNode != 0 && int(resources.SocketsPerNode) != sockets) || (resources.CoresPerSocket != 0 && int(resources.CoresPerSocket) != cores) {
		return nil, fmt.Errorf("allocated Slurm geometry differs from applied CPU mapping")
	}
	bitmap, err := bitmaputil.NewFrom(resources.CoreBitmap)
	if err != nil {
		return nil, err
	}
	var invalid bool
	bitmap.Range(func(idx uint32) {
		if uint64(idx) >= uint64(len(n.CpuMap.AbstractToMachine)) {
			invalid = true
		}
	})
	if invalid {
		return nil, fmt.Errorf("allocated Slurm bitmap contains a core outside the applied CPU mapping")
	}
	cpus := n.CpuMap.ToMachineCPUs(bitmap)
	if cpus.IsEmpty() {
		return nil, fmt.Errorf("empty Slurm CPU allocation")
	}
	bySocket := make(map[int]cpuset.CPUSet)
	for _, cpu := range cpus.List() {
		info, ok := n.CpuMap.CPUInfoMap[cpu]
		if !ok {
			return nil, fmt.Errorf("CPU %d missing from applied mapping", cpu)
		}
		bySocket[info.SocketID] = bySocket[info.SocketID].Union(cpuset.New(cpu))
	}
	allocation := &CPUAllocation{CPUCount: int64(cpus.Size())}
	celCache := dracel.NewCache(8, dracel.Features{EnableConsumableCapacity: true, EnableListTypeAttributes: true})
	for _, socket := range slices.Sorted(maps.Keys(bySocket)) {
		device, ok := n.SocketDevices[socket]
		if !ok {
			return nil, fmt.Errorf("socket %d has no applied device", socket)
		}
		for _, selector := range class.Spec.Selectors {
			if selector.CEL == nil {
				return nil, fmt.Errorf("CPU DeviceClass has an unsupported selector")
			}
			compiled := celCache.GetOrCompile(selector.CEL.Expression)
			if compiled.Error != nil {
				return nil, compiled.Error
			}
			matched, _, err := compiled.DeviceMatches(ctx, dracel.Device{Driver: DraDriverCpu, Attributes: device.Attributes, Capacity: device.Capacity, AllowMultipleAllocations: device.AllowMultipleAllocations})
			if err != nil {
				return nil, err
			}
			if !matched {
				return nil, fmt.Errorf("socket device %q does not match CPU DeviceClass %q", device.Name, className)
			}
		}
		set := bySocket[socket]
		count := *resource.NewQuantity(int64(set.Size()), resource.DecimalSI)
		capacity := device.Capacity[DraDriverCpu_Capacity]
		if capacity.Value.Cmp(count) < 0 {
			return nil, fmt.Errorf("CPU allocation exceeds socket %d capacity", socket)
		}
		name := fmt.Sprintf("cpu-socket-%d", socket)
		allocation.Requests = append(allocation.Requests, resourcev1.DeviceRequest{Name: name, Exactly: &resourcev1.ExactDeviceRequest{
			DeviceClassName: className, AllocationMode: resourcev1.DeviceAllocationModeExactCount, Count: 1,
			Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: fmt.Sprintf("device.driver == 'dra.cpu' && device.attributes['dra.cpu'].socketID == %d", socket)}}},
			Capacity:  &resourcev1.CapacityRequirements{Requests: map[resourcev1.QualifiedName]resource.Quantity{DraDriverCpu_Capacity: count}},
		}})
		share := types.UID(uuid.NewSHA1(uuid.NameSpaceOID, []byte(allocationKey+"/"+DraDriverCpu+"/"+n.CpuMap.Pool+"/"+device.Name)).String())
		allocation.Results = append(allocation.Results, resourcev1.DeviceRequestAllocationResult{Request: name, Driver: DraDriverCpu, Pool: n.CpuMap.Pool, Device: device.Name, ShareID: ptr.To(share), ConsumedCapacity: map[resourcev1.QualifiedName]resource.Quantity{DraDriverCpu_Capacity: count}})
		parameters, err := json.Marshal(struct {
			APIVersion string `json:"apiVersion"`
			CPUConfig  struct {
				CPUSet string `json:"cpuset"`
			} `json:"cpuConfig"`
		}{APIVersion: "v1alpha1", CPUConfig: struct {
			CPUSet string `json:"cpuset"`
		}{CPUSet: set.String()}})
		if err != nil {
			return nil, err
		}
		allocation.Config = append(allocation.Config, resourcev1.DeviceClaimConfiguration{Requests: []string{name}, DeviceConfiguration: resourcev1.DeviceConfiguration{Opaque: &resourcev1.OpaqueDeviceConfiguration{Driver: DraDriverCpu, Parameters: runtime.RawExtension{Raw: parameters}}}})
	}
	return allocation, nil
}
