// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"k8s.io/apimachinery/pkg/api/equality"
)

func TestNewGPUMap(t *testing.T) {
	tests := []struct {
		name     string
		gpuInfos []*nodeinfo.GPUInfo
		want     nodeinfo.GPUMap
	}{
		{
			name: "smoke",
			gpuInfos: []*nodeinfo.GPUInfo{
				{Name: "gpu0", Index: 0},
				{Name: "gpu1", Index: 1},
			},
			want: nodeinfo.GPUMap{
				GPUInfoMap: map[int]*nodeinfo.GPUInfo{
					0: {Name: "gpu0", Index: 0},
					1: {Name: "gpu1", Index: 1},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeinfo.NewGPUMap(tt.gpuInfos)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("NewGPUMap() = %v, want %v", got, tt.want)
			}
		})
	}
}
