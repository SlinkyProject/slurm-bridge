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
		driver   string
		gpuInfos []*nodeinfo.GPUInfo
		want     nodeinfo.GPUMap
	}{
		{
			name:   "smoke",
			driver: nodeinfo.DraExampleDriver,
			gpuInfos: []*nodeinfo.GPUInfo{
				{Name: "gpu0", Index: 0},
				{Name: "gpu1", Index: 1},
			},
			want: nodeinfo.GPUMap{
				Driver: nodeinfo.DraExampleDriver,
				GPUInfoMap: map[int]*nodeinfo.GPUInfo{
					0: {Name: "gpu0", Index: 0},
					1: {Name: "gpu1", Index: 1},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeinfo.NewGPUMap(tt.driver, tt.gpuInfos)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("NewGPUMap() = %v, want %v", got, tt.want)
			}
		})
	}
}
