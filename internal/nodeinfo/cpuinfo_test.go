// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
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
								nodeinfo.DraDriverCpu_CoreType: {StringValue: ptr.To(nodeinfo.CoreTypeStandard.String())},
							},
						},
						{
							Name: "cpu1",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](1)},
								nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreType: {StringValue: ptr.To(nodeinfo.CoreTypeStandard.String())},
							},
						},
						{
							Name: "cpu2",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](2)},
								nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
								nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreType: {StringValue: ptr.To(nodeinfo.CoreTypeStandard.String())},
							},
						},
						{
							Name: "cpu3",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](3)},
								nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
								nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
								nodeinfo.DraDriverCpu_CoreType: {StringValue: ptr.To(nodeinfo.CoreTypeStandard.String())},
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
			got, err := nodeinfo.NewCPUInfos(tt.rSlice)
			if err != nil {
				t.Fatalf("NewCPUInfos() error = %v", err)
			}
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("NewCPUInfos() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewCPUInfosRejectsUnsupportedSchemas(t *testing.T) {
	device := resourcev1.Device{
		Name: "cpudev000",
		Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](0)},
			nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
			nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
			nodeinfo.DraDriverCpu_CoreType: {StringValue: ptr.To("standard")},
		},
	}
	tests := []struct {
		name    string
		mutate  func(*resourcev1.Device)
		wantErr string
	}{
		{
			name: "grouped device",
			mutate: func(device *resourcev1.Device) {
				delete(device.Attributes, nodeinfo.DraDriverCpu_CpuID)
				delete(device.Attributes, nodeinfo.DraDriverCpu_CoreID)
				device.Attributes["dra.cpu/numCPUs"] = resourcev1.DeviceAttribute{IntValue: ptr.To[int64](4)}
			},
			wantErr: "individual-device schema",
		},
		{
			name: "integer core type",
			mutate: func(device *resourcev1.Device) {
				device.Attributes[nodeinfo.DraDriverCpu_CoreType] = resourcev1.DeviceAttribute{IntValue: ptr.To[int64](1)}
			},
			wantErr: `attribute "dra.cpu/coreType" must be a string`,
		},
		{
			name: "unknown core type",
			mutate: func(device *resourcev1.Device) {
				device.Attributes[nodeinfo.DraDriverCpu_CoreType] = resourcev1.DeviceAttribute{StringValue: ptr.To("turbo")}
			},
			wantErr: `unsupported core type "turbo"`,
		},
		{
			name: "negative CPU ID",
			mutate: func(device *resourcev1.Device) {
				device.Attributes[nodeinfo.DraDriverCpu_CpuID] = resourcev1.DeviceAttribute{IntValue: ptr.To[int64](-1)}
			},
			wantErr: "must not be negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := device.DeepCopy()
			tt.mutate(device)
			_, err := nodeinfo.NewCPUInfos(&resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "node-cpus"},
				Spec: resourcev1.ResourceSliceSpec{
					Driver:  nodeinfo.DraDriverCpu,
					Devices: []resourcev1.Device{*device},
				},
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewCPUInfos() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
