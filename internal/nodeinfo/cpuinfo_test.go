// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestNewCPUInfos(t *testing.T) {
	tests := []struct {
		name   string
		rSlice *resourcev1.ResourceSlice
		want   []*nodeinfo.CPUInfo
	}{
		{
			name: "dra.cpu",
			rSlice: &resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
				Spec: resourcev1.ResourceSliceSpec{
					NodeName: ptr.To("node"),
					Driver:   nodeinfo.DraDriverCpu,
					Devices: []resourcev1.Device{
						{
							Name: "cpu0",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
							},
						},
						{
							Name: "cpu1",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](1)},
								nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
							},
						},
						{
							Name: "cpu2",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](2)},
								nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
								nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
							},
						},
						{
							Name: "cpu3",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](3)},
								nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
								nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
							},
						},
					},
				},
			},
			want: []*nodeinfo.CPUInfo{
				{Name: "cpu0", CpuID: 0, CoreID: 0, SocketID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu1", CpuID: 1, CoreID: 0, SocketID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu2", CpuID: 2, CoreID: 1, SocketID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu3", CpuID: 3, CoreID: 1, SocketID: 0, CoreType: nodeinfo.CoreTypeStandard},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeinfo.NewCPUInfos(tt.rSlice)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("NewCPUInfos() = %v, want %v", got, tt.want)
			}
		})
	}
}
