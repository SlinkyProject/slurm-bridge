// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/puttsk/hostlist"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

// Represents a Kubernetes node for Slurm.
type NodeInfo struct {
	cpuMap CPUMap
	gpuMap GPUMap
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
		cpuSet := n.cpuMap.ToMachineCPUs(bitmap)
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
		indexListString := strings.Join(indexList, ",")
		req := resourcev1.DeviceRequest{
			Name: gres.Name,
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: deviceClassName,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           gres.Count,
				Selectors: []resourcev1.DeviceSelector{
					{
						CEL: &resourcev1.CELDeviceSelector{
							Expression: fmt.Sprintf("device.attributes['%s'].index in [%s]", deviceClassName, indexListString),
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
		cpuSet := n.cpuMap.ToMachineCPUs(bitmap)
		for _, cpuID := range cpuSet.List() {
			cpuInfo := n.cpuMap.CPUInfoMap[cpuID]
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
			gpuInfo := n.gpuMap.GPUInfoMap[index]
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
	nodeNameOpt := client.MatchingFields{resourcev1.ResourceSliceSelectorNodeName: nodeName}
	if err := kubeclient.List(ctx, resourceSliceList, nodeNameOpt); err != nil {
		return nil, err
	}

	nodeInfo := &NodeInfo{}
	for _, resourceSlice := range resourceSliceList.Items {
		switch resourceSlice.Spec.Driver {
		case DraDriverCpu:
			cpuInfos := NewCPUInfos(&resourceSlice)
			nodeInfo.cpuMap = NewCPUMap(cpuInfos)
		case DraExampleDriver:
			gpuInfos := NewGPUInfos(&resourceSlice)
			nodeInfo.gpuMap = NewGPUMap(gpuInfos)
		default:
			// TODO: can we even default?
		}
	}

	return nodeInfo, nil
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
