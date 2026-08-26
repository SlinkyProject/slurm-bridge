// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

func TestLegacyGPUDeviceRequests(t *testing.T) {
	kubeClient := fake.NewClientBuilder().WithObjects(
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: legacyDRAExampleDriver}},
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: legacyDRANVIDIADriver}},
	).Build()
	resources := []slurmcontrol.GresLayout{
		{Name: "gpu-example", Type: legacyDRAExampleDriver, Count: 2, Index: "0-1"},
		{Name: "gpu-nvidia", Type: legacyDRANVIDIADriver, Count: 1, Index: "2"},
		{Name: "unknown", Type: "gpu.unknown.example", Count: 1, Index: "0"},
	}

	got, err := legacyGPUDeviceRequests(context.Background(), kubeClient, resources)
	if err != nil {
		t.Fatalf("legacyGPUDeviceRequests() error = %v", err)
	}
	want := []resourcev1.DeviceRequest{
		{
			Name: "gpu-example",
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: legacyDRAExampleDriver,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           2,
				Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{
					Expression: "device.attributes['gpu.example.com'].index in [0,1]",
				}}},
			},
		},
		{
			Name: "gpu-nvidia",
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: legacyDRANVIDIADriver,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           1,
				Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{
					Expression: "device.attributes['gpu.nvidia.com'].name in ['gpu-2']",
				}}},
			},
		},
	}
	if !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("legacyGPUDeviceRequests() = %#v, want %#v", got, want)
	}
}

func TestLegacyGPUDeviceRequestsRequiresIndexes(t *testing.T) {
	kubeClient := fake.NewClientBuilder().WithObjects(
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: legacyDRAExampleDriver}},
	).Build()
	_, err := legacyGPUDeviceRequests(context.Background(), kubeClient, []slurmcontrol.GresLayout{{
		Name: "gpu", Type: legacyDRAExampleDriver, Count: 1,
	}})
	if err == nil || !strings.Contains(err.Error(), "missing indexes") {
		t.Fatalf("legacyGPUDeviceRequests() error = %v, want missing indexes", err)
	}
}

func TestLegacyGPUAllocationResults(t *testing.T) {
	kubeClient := fake.NewClientBuilder().WithObjects(
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: legacyDRAExampleDriver}},
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: legacyDRANVIDIADriver}},
		&resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "example-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				NodeName: ptr.To("node"),
				Pool:     resourcev1.ResourcePool{Name: "example-pool"},
				Driver:   legacyDRAExampleDriver,
				Devices: []resourcev1.Device{
					{Name: "example-0", Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						legacyDRAExampleGPUIndexAttribute: {IntValue: ptr.To[int64](0)},
					}},
					{Name: "example-1", Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						legacyDRAExampleGPUIndexAttribute: {IntValue: ptr.To[int64](1)},
					}},
				},
			},
		},
		&resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "nvidia-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				NodeName: ptr.To("node"),
				Pool:     resourcev1.ResourcePool{Name: "nvidia-pool"},
				Driver:   legacyDRANVIDIADriver,
				Devices:  []resourcev1.Device{{Name: "gpu-2"}},
			},
		},
	).Build()
	resources := []slurmcontrol.GresLayout{
		{Name: "example", Type: legacyDRAExampleDriver, Count: 1, Index: "1"},
		{Name: "nvidia", Type: legacyDRANVIDIADriver, Count: 1, Index: "2"},
	}

	got, err := legacyGPUAllocationResults(context.Background(), kubeClient, "node", resources)
	if err != nil {
		t.Fatalf("legacyGPUAllocationResults() error = %v", err)
	}
	want := []resourcev1.DeviceRequestAllocationResult{
		{Request: "example", Driver: legacyDRAExampleDriver, Pool: "example-pool", Device: "example-1"},
		{Request: "nvidia", Driver: legacyDRANVIDIADriver, Pool: "nvidia-pool", Device: "gpu-2"},
	}
	if !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("legacyGPUAllocationResults() = %#v, want %#v", got, want)
	}
}

func TestLegacyGPUAllocationResultsRejectsMissingDevice(t *testing.T) {
	kubeClient := fake.NewClientBuilder().WithObjects(
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: legacyDRANVIDIADriver}},
		&resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "nvidia-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				NodeName: ptr.To("node"),
				Pool:     resourcev1.ResourcePool{Name: "node"},
				Driver:   legacyDRANVIDIADriver,
				Devices:  []resourcev1.Device{{Name: "gpu-0"}},
			},
		},
	).Build()
	_, err := legacyGPUAllocationResults(context.Background(), kubeClient, "node", []slurmcontrol.GresLayout{{
		Name: "gpu", Type: legacyDRANVIDIADriver, Count: 1, Index: "1",
	}})
	if err == nil || !strings.Contains(err.Error(), "not found on node") {
		t.Fatalf("legacyGPUAllocationResults() error = %v, want missing device", err)
	}
}
