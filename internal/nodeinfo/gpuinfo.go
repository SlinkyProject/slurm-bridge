// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"fmt"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/utils/ptr"
)

// GPUInfo holds information about a single GPU.
type GPUInfo struct {
	// Name of the GPU device
	Name string `json:"name"`

	// Index is the GPU Index
	Index int `json:"index"`
}

const DraExampleDriver = "gpu.example.com"

// Resource attributes
const (
	DraExampleDriver_Index resourcev1.QualifiedName = "index"
)

func NewGPUInfos(rSlice *resourcev1.ResourceSlice) []*GPUInfo {
	gpuInfos := []*GPUInfo{}
	switch rSlice.Spec.Driver {
	case DraExampleDriver:
		for _, device := range rSlice.Spec.Devices {
			cpuInfo := &GPUInfo{
				Name:  device.Name,
				Index: int(ptr.Deref(device.Attributes[DraExampleDriver_Index].IntValue, -1)),
			}
			gpuInfos = append(gpuInfos, cpuInfo)
		}
	default:
		panic(fmt.Errorf("unsupported resource device driver: %v", rSlice.Spec.Driver))
	}
	return gpuInfos
}
