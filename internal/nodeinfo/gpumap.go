// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import "sort"

type GPUMap struct {
	// GPUInfoMap stores the raw GPUInfos as a map,
	// where the index is the GPU index, and the value is the GPUInfo.
	GPUInfoMap map[int]*GPUInfo `json:"gpuInfoMap"`
}

func NewGPUMap(gpuInfos []*GPUInfo) GPUMap {
	sort.SliceStable(gpuInfos, func(i, j int) bool {
		return gpuInfos[i].Index < gpuInfos[j].Index
	})

	gpuInfoMap := make(map[int]*GPUInfo, len(gpuInfos))
	for _, gpuInfo := range gpuInfos {
		gpuInfoMap[gpuInfo.Index] = gpuInfo
	}
	return GPUMap{
		GPUInfoMap: gpuInfoMap,
	}
}
