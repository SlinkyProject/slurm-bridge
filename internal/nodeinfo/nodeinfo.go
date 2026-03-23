// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/puttsk/hostlist"
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
	GpuMap GPUMap
}

func (n *NodeInfo) GetDeviceRequests(ctx context.Context, kubeclient client.Client, resources *slurmcontrol.NodeResources) ([]resourcev1.DeviceRequest, error) {
	var requests []resourcev1.DeviceRequest

	if resources == nil {
		return requests, nil
	}

	if hasDeviceClass(ctx, kubeclient, DraDriverCpu) {
		bitmap, err := bitmaputil.NewFrom(resources.CoreBitmap)
		if err != nil {
			return nil, err
		}
		cpuSet := n.CpuMap.ToMachineCPUs(bitmap)
		cpuSetString := strings.ReplaceAll(fmt.Sprint(cpuSet.List()), " ", ",")
		req := resourcev1.DeviceRequest{
			Name: corev1.ResourceCPU.String(),
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: DraDriverCpu,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           int64(cpuSet.Size()),
				Selectors: []resourcev1.DeviceSelector{
					{
						CEL: &resourcev1.CELDeviceSelector{
							Expression: fmt.Sprintf("device.attributes['%s'].cpuID in %s", DraDriverCpu, cpuSetString),
						},
					},
				},
			},
		}
		requests = append(requests, req)
	}

	for _, gres := range resources.Gres {
		deviceClassName := gres.Type
		if !hasDeviceClass(ctx, kubeclient, gres.Type) {
			continue
		}
		indexList, err := hostlist.Expand(fmt.Sprintf("[%s]", gres.Index))
		if err != nil {
			return nil, err
		}
		var celExpr string
		switch deviceClassName {
		case DraDriverGpuNvidia:
			// NVIDIA k8s-dra-driver-gpu: use device.attributes['gpu.nvidia.com'].name (e.g. "gpu-0", "gpu-1").
			names := make([]string, 0, len(indexList))
			for _, i := range indexList {
				names = append(names, fmt.Sprintf("'gpu-%s'", i))
			}
			celExpr = fmt.Sprintf("device.attributes['%s'].name in [%s]", DraDriverGpuNvidia, strings.Join(names, ","))
		case DraExampleDriver:
			// Example DRA driver: use device.attributes['gpu.example.com'].index (e.g. 0, 1, 2).
			indexListString := strings.Join(indexList, ",")
			celExpr = fmt.Sprintf("device.attributes['%s'].index in [%s]", deviceClassName, indexListString)
		default:
			continue
		}
		req := resourcev1.DeviceRequest{
			Name: gres.Name,
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: deviceClassName,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           gres.Count,
				Selectors: []resourcev1.DeviceSelector{
					{
						CEL: &resourcev1.CELDeviceSelector{
							Expression: celExpr,
						},
					},
				},
			},
		}
		requests = append(requests, req)
	}

	return requests, nil
}

func (n *NodeInfo) GetDeviceRequestAllocationResult(ctx context.Context, kubeclient client.Client, resources *slurmcontrol.NodeResources) ([]resourcev1.DeviceRequestAllocationResult, error) {
	var devices []resourcev1.DeviceRequestAllocationResult

	if resources == nil {
		return devices, nil
	}

	if hasDeviceClass(ctx, kubeclient, DraDriverCpu) {
		bitmap, err := bitmaputil.NewFrom(resources.CoreBitmap)
		if err != nil {
			return nil, err
		}
		// Individual Mode: each CPU is enumerated
		cpuSet := n.CpuMap.ToMachineCPUs(bitmap)
		for _, cpuID := range cpuSet.List() {
			cpuInfo, ok := n.CpuMap.CPUInfoMap[cpuID]
			if !ok {
				continue
			}
			dev := resourcev1.DeviceRequestAllocationResult{
				Request: corev1.ResourceCPU.String(),
				Driver:  DraDriverCpu,
				Pool:    resources.Node,
				Device:  cpuInfo.Name,
			}
			devices = append(devices, dev)
		}
	}

	for _, gres := range resources.Gres {
		deviceClassName := gres.Type
		if !hasDeviceClass(ctx, kubeclient, gres.Type) {
			continue
		}
		indexList, err := hostlist.Expand(fmt.Sprintf("[%s]", gres.Index))
		if err != nil {
			return nil, err
		}
		for _, i := range indexList {
			index, err := strconv.Atoi(i)
			if err != nil {
				return nil, err
			}
			gpuInfo, ok := n.GpuMap.GPUInfoMap[index]
			if !ok {
				continue
			}
			dev := resourcev1.DeviceRequestAllocationResult{
				Request: gres.Name,
				Driver:  deviceClassName,
				Pool:    resources.Node,
				Device:  gpuInfo.Name,
			}
			devices = append(devices, dev)
		}
	}

	return devices, nil
}

func NewNodeInfo(ctx context.Context, kubeclient client.Client, nodeName string) (*NodeInfo, error) {
	resourceSliceList := &resourcev1.ResourceSliceList{}
	if err := kubeclient.List(ctx, resourceSliceList); err != nil {
		return nil, err
	}

	nodeInfo := &NodeInfo{}
	for _, resourceSlice := range resourceSliceList.Items {
		if ptr.Deref(resourceSlice.Spec.NodeName, "") != nodeName {
			continue
		}
		switch resourceSlice.Spec.Driver {
		case DraDriverCpu:
			cpuInfos := NewCPUInfos(&resourceSlice)
			nodeInfo.CpuMap = NewCPUMap(cpuInfos)
		case DraExampleDriver, DraDriverGpuNvidia:
			gpuInfos := NewGPUInfos(ctx, &resourceSlice)
			nodeInfo.GpuMap = NewGPUMap(resourceSlice.Spec.Driver, gpuInfos)
		default:
			// TODO: can we even default?
		}
	}

	return nodeInfo, nil
}

// GetGresAndGresConf returns Slurm GRES and GresConf strings for this node's devices.
// GRES and GresConf are derived from DRA ResourceSlices (e.g. GPU devices); CPU is not included.
// Returns ("", "") when the node has no GRES devices.
func (n *NodeInfo) GetGresAndGresConf() (gres, gresConf string) {
	if len(n.GpuMap.GPUInfoMap) == 0 {
		return "", ""
	}
	// Build gres: "gpu:driver:count"
	count := len(n.GpuMap.GPUInfoMap)
	gres = fmt.Sprintf("gpu:%s:%d", n.GpuMap.Driver, count)

	// Build gresConf: count=N,name=gpu,type=driver,file=name0,file=name1,...
	// Slurm requires count= and one file= per device for create node to succeed.
	indices := make([]int, 0, count)
	for idx := range n.GpuMap.GPUInfoMap {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	fileParts := make([]string, 0, count)
	for _, idx := range indices {
		info := n.GpuMap.GPUInfoMap[idx]
		deviceName := fmt.Sprintf("gpu-%d", idx)
		if info != nil && info.Name != "" {
			deviceName = info.Name
		}
		fileParts = append(fileParts, "file="+deviceName)
	}
	gresConf = fmt.Sprintf("count=%d,name=gpu,type=%s,%s", count, n.GpuMap.Driver, strings.Join(fileParts, ","))
	return gres, gresConf
}

func hasDeviceClass(ctx context.Context, kubeclient client.Client, deviceClassName string) bool {
	if deviceClassName == "" {
		return false
	}
	deviceClass := &resourcev1.DeviceClass{}
	err := kubeclient.Get(ctx, types.NamespacedName{Name: deviceClassName}, deviceClass)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		return false
	}
	return true
}
