// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// GPUInfo holds information about a single GPU.
type GPUInfo struct {
	// Name of the GPU device
	Name string `json:"name"`

	// Index is the GPU Index
	Index int `json:"index"`
}

const (
	DraExampleDriver   = "gpu.example.com"
	DraDriverGpuNvidia = "gpu.nvidia.com"
)

// Resource attributes. DraExampleDriver uses "index" in Device.Attributes.
// DraDriverGpuNvidia uses Name "gpu-<minor>", (e.g. "gpu-0", "gpu-1" from
// CanonicalName in NVIDIA k8s-dra-driver-gpu).
const (
	DraExampleDriver_Index resourcev1.QualifiedName = "index"
)

func NewGPUInfos(ctx context.Context, rSlice *resourcev1.ResourceSlice) []*GPUInfo {
	logger := log.FromContext(ctx)
	gpuInfos := []*GPUInfo{}
	switch rSlice.Spec.Driver {
	case DraExampleDriver:
		for _, device := range rSlice.Spec.Devices {
			gpuInfo := &GPUInfo{
				Name:  device.Name,
				Index: int(ptr.Deref(device.Attributes[DraExampleDriver_Index].IntValue, -1)),
			}
			gpuInfos = append(gpuInfos, gpuInfo)
		}
	case DraDriverGpuNvidia:
		for _, device := range rSlice.Spec.Devices {
			index := nvidiaGpuNameToIndex(device.Name)
			if index >= 0 {
				gpuInfos = append(gpuInfos, &GPUInfo{
					Name:  device.Name,
					Index: index,
				})
			} else {
				logger.V(1).Info("invalid NVIDIA GPU device name, expected gpu-<non-negative int>", "deviceName", device.Name)
			}
		}
	default:
		panic(fmt.Errorf("unsupported resource device driver: %v", rSlice.Spec.Driver))
	}
	return gpuInfos
}

// nvidiaGpuNameToIndex returns the GPU index from the NVIDIA DRA driver device
// name ("gpu-0", "gpu-1", ...). Returns -1 if name is not "gpu-<non-negative int>".
func nvidiaGpuNameToIndex(name string) int {
	if s := strings.TrimPrefix(name, "gpu-"); s != name {
		if i, err := strconv.Atoi(s); err == nil && i >= 0 {
			return i
		}
	}
	return -1
}
