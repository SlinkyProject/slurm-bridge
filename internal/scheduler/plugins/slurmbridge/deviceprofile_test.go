// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"slices"
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

func TestAllocateCoreBitmapProfileUsesDeviceClassAlias(t *testing.T) {
	profile, ok := dra.DefaultRegistry().LookupByName("cpu")
	if !ok {
		t.Fatal("default registry does not contain cpu")
	}

	allocation, err := allocateCoreBitmapProfile([]deviceProfileRequest{{
		DeviceClassName: "my-cpus",
		Profile:         profile,
		Count:           2,
	}})
	if err != nil {
		t.Fatalf("allocateCoreBitmapProfile() error = %v", err)
	}
	if allocation == nil || allocation.DeviceClassName != "my-cpus" || allocation.RequestName != "cpu" || allocation.Count != 2 {
		t.Fatalf("allocateCoreBitmapProfile() = %#v, want my-cpus allocation", allocation)
	}
}

func TestAllocateCoreBitmapProfileRejectsMultipleDeviceClasses(t *testing.T) {
	profile, ok := dra.DefaultRegistry().LookupByName("cpu")
	if !ok {
		t.Fatal("default registry does not contain cpu")
	}

	_, err := allocateCoreBitmapProfile([]deviceProfileRequest{
		{DeviceClassName: "class-a", Profile: profile, Count: 1},
		{DeviceClassName: "class-b", Profile: profile, Count: 1},
	})
	if err == nil || !strings.Contains(err.Error(), `multiple DeviceClasses "class-a" and "class-b"`) {
		t.Fatalf("allocateCoreBitmapProfile() error = %v, want multiple DeviceClasses error", err)
	}
}

func TestAllocateIndexedGRESProfilesPartitionsAliases(t *testing.T) {
	profile, ok := dra.DefaultRegistry().LookupByName("gpu-example")
	if !ok {
		t.Fatal("default registry does not contain gpu-example")
	}
	requests := []deviceProfileRequest{
		{DeviceClassName: "class-a", Profile: profile, Count: 1},
		{DeviceClassName: "class-b", Profile: profile, Count: 2},
	}
	allocations, err := allocateIndexedGRESProfiles(requests, []slurmcontrol.GresLayout{{
		Name: "gpu", Type: "gpu-example", Count: 4, Index: "3,1,0,2",
	}})
	if err != nil {
		t.Fatalf("allocateIndexedGRESProfiles() error = %v", err)
	}
	if len(allocations) != 2 || !slices.Equal(allocations[0].Indexes, []int{3}) || !slices.Equal(allocations[1].Indexes, []int{1, 0}) {
		t.Fatalf("allocateIndexedGRESProfiles() = %#v, want indexes [3] and [1 0]", allocations)
	}
}

func TestVerifyIndexedGRESDeviceProfileRequestRejectsChangedCount(t *testing.T) {
	profile, ok := dra.DefaultRegistry().LookupByName("gpu-example")
	if !ok {
		t.Fatal("default registry does not contain gpu-example")
	}
	sb := &SlurmBridge{
		Client: fake.NewClientBuilder().WithObjects(&resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "example-gpus"},
			Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "gpu.example.com"`},
			}}},
		}).Build(),
		draRegistry: dra.DefaultRegistry(),
	}
	claim := &resourcev1.ResourceClaim{Spec: resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{
		Requests: []resourcev1.DeviceRequest{{
			Name: "gpu",
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: "example-gpus",
				Count:           4,
			},
		}},
	}}}
	_, err := sb.verifyDeviceProfileRequest(context.Background(), claim, deviceProfileRequest{
		DeviceClassName: "example-gpus",
		Profile:         profile,
		Count:           1,
	}, "gpu")
	if err == nil || !strings.Contains(err.Error(), "has count 4, expected exactly 1") {
		t.Fatalf("verifyDeviceProfileRequest() error = %v, want changed count error", err)
	}
}

func TestVerifyCoreBitmapRequestUsesAllocatedThreadCount(t *testing.T) {
	profile, ok := dra.DefaultRegistry().LookupByName("cpu")
	if !ok {
		t.Fatal("default registry does not contain cpu")
	}
	sb := &SlurmBridge{
		Client: fake.NewClientBuilder().WithObjects(&resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "my-cpus"},
			Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "dra.cpu"`},
			}}},
		}).Build(),
		draRegistry: dra.DefaultRegistry(),
	}
	claim := &resourcev1.ResourceClaim{Spec: resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{
		Requests: []resourcev1.DeviceRequest{{
			Name: "cpu",
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: "my-cpus",
				Count:           4,
			},
		}},
	}}}
	allocation := &coreBitmapAllocation{
		deviceProfileRequest: deviceProfileRequest{
			DeviceClassName: "my-cpus",
			Profile:         profile,
			Count:           3,
		},
		RequestName:    "cpu",
		AllocatedCount: 4,
	}
	if err := sb.verifyCoreBitmapRequest(context.Background(), claim, allocation); err != nil {
		t.Fatalf("verifyCoreBitmapRequest() error = %v", err)
	}
}

func TestValidateDeviceProfileAllocationCounts(t *testing.T) {
	allocation := &claimAllocation{
		CoreBitmapAllocation: &coreBitmapAllocation{
			deviceProfileRequest: deviceProfileRequest{Count: 1},
			RequestName:          "cpu",
			AllocatedCount:       2,
		},
		IndexedGRESAllocations: []indexedGRESAllocation{{
			deviceProfileRequest: deviceProfileRequest{Count: 2},
			RequestName:          "gpu",
		}},
	}
	results := []resourcev1.DeviceRequestAllocationResult{
		{Request: "cpu"},
		{Request: "cpu"},
		{Request: "gpu"},
		{Request: "gpu"},
	}
	if err := validateDeviceProfileAllocationCounts(allocation, results); err != nil {
		t.Fatalf("validateDeviceProfileAllocationCounts() error = %v", err)
	}
	results = append(results, resourcev1.DeviceRequestAllocationResult{Request: "gpu"})
	if err := validateDeviceProfileAllocationCounts(allocation, results); err == nil || !strings.Contains(err.Error(), `request "gpu" allocated 3 devices, expected exactly 2`) {
		t.Fatalf("validateDeviceProfileAllocationCounts() error = %v, want excess allocation error", err)
	}
}

func TestAllocateIndexedGRESProfilesRejectsMissingAllocation(t *testing.T) {
	profile, ok := dra.DefaultRegistry().LookupByName("gpu-example")
	if !ok {
		t.Fatal("default registry does not contain gpu-example")
	}

	_, err := allocateIndexedGRESProfiles([]deviceProfileRequest{{
		DeviceClassName: "example-gpus",
		Profile:         profile,
		Count:           1,
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), `DeviceClass "example-gpus" resolves to DeviceProfile "gpu-example" but the Slurm allocation has no matching indexed GRES`) {
		t.Fatalf("allocateIndexedGRESProfiles() error = %v, want missing indexed GRES error", err)
	}
}

func TestAllocateIndexedGRESProfilesUsesNVIDIAProfile(t *testing.T) {
	profile, ok := dra.DefaultRegistry().LookupByName("gpu-nvidia")
	if !ok {
		t.Fatal("default registry does not contain gpu-nvidia")
	}
	allocations, err := allocateIndexedGRESProfiles([]deviceProfileRequest{{
		DeviceClassName: "gpu.nvidia.com",
		Profile:         profile,
		Count:           2,
	}}, []slurmcontrol.GresLayout{{
		Name: "gpu", Type: "gpu-nvidia", Count: 2, Index: "3,1",
	}})
	if err != nil {
		t.Fatalf("allocateIndexedGRESProfiles() error = %v", err)
	}
	if len(allocations) != 1 || !slices.Equal(allocations[0].Indexes, []int{3, 1}) {
		t.Fatalf("allocateIndexedGRESProfiles() = %#v, want indexes [3 1]", allocations)
	}
}

func TestAllocateIndexedGRESProfilesIgnoresOtherBackends(t *testing.T) {
	requests := []deviceProfileRequest{{
		DeviceClassName: "cpu-class",
		Profile: dra.DeviceProfile{
			Name:    "cpu",
			Backend: dra.CoreBitmapBackend{},
		},
		Count: 2,
	}}

	allocations, err := allocateIndexedGRESProfiles(requests, nil)
	if err != nil {
		t.Fatalf("allocateIndexedGRESProfiles() error = %v", err)
	}
	if len(allocations) != 0 {
		t.Fatalf("allocateIndexedGRESProfiles() = %#v, want no allocations", allocations)
	}
}

func TestSplitGRESResourcesUsesAllocatedRepresentation(t *testing.T) {
	resources := slurmcontrol.NodeResources{
		Node:      "node-a",
		NodeExtra: "inventory-extra",
		Gres: []slurmcontrol.GresLayout{
			{Name: "gpu", Type: "gpu-example", Count: 2, Index: "0-1"},
			{Name: "gpu", Type: "gpu-nvidia", Count: 1, Index: "1"},
			{Name: "gpu", Type: "gpu.example.com", Count: 1, Index: "2"},
			{Name: "gpu", Type: "gpu.nvidia.com", Count: 1, Index: "3"},
			{Name: "license", Type: "matlab", Count: 1},
		},
	}

	indexedGRESResources, remainingResources, err := splitGRESResources(dra.DefaultRegistry(), resources)
	if err != nil {
		t.Fatalf("splitGRESResources() error = %v", err)
	}
	wantProfile := []slurmcontrol.GresLayout{
		{Name: "gpu", Type: "gpu-example", Count: 2, Index: "0-1"},
		{Name: "gpu", Type: "gpu-nvidia", Count: 1, Index: "1"},
	}
	wantNonProfile := []slurmcontrol.GresLayout{
		{Name: "gpu", Type: "gpu.example.com", Count: 1, Index: "2"},
		{Name: "gpu", Type: "gpu.nvidia.com", Count: 1, Index: "3"},
		{Name: "license", Type: "matlab", Count: 1},
	}
	if !slices.Equal(indexedGRESResources.Gres, wantProfile) {
		t.Errorf("indexed GRES resources = %#v, want %#v", indexedGRESResources.Gres, wantProfile)
	}
	if !slices.Equal(remainingResources.Gres, wantNonProfile) {
		t.Errorf("remaining resources = %#v, want %#v", remainingResources.Gres, wantNonProfile)
	}
	if indexedGRESResources.Node != resources.Node || remainingResources.Node != resources.Node ||
		indexedGRESResources.NodeExtra != resources.NodeExtra || remainingResources.NodeExtra != resources.NodeExtra {
		t.Fatalf("split resources did not preserve node metadata: indexed=%#v remaining=%#v", indexedGRESResources, remainingResources)
	}
	if len(resources.Gres) != 5 {
		t.Fatalf("splitGRESResources() mutated its input: %#v", resources.Gres)
	}
}

func TestSplitGRESResourcesRejectsWrongProfileGRESName(t *testing.T) {
	_, _, err := splitGRESResources(dra.DefaultRegistry(), slurmcontrol.NodeResources{
		Gres: []slurmcontrol.GresLayout{{Name: "accelerator", Type: "gpu-example", Count: 1, Index: "0"}},
	})
	if err == nil {
		t.Fatal("splitGRESResources() error = nil, want profile GRES name mismatch")
	}
}

func TestSplitGRESResourcesDoesNotClaimCoreBitmapProfileName(t *testing.T) {
	resource := slurmcontrol.GresLayout{Name: "gpu", Type: "cpu", Count: 1, Index: "0"}
	indexedGRESResources, remainingResources, err := splitGRESResources(dra.DefaultRegistry(), slurmcontrol.NodeResources{
		Gres: []slurmcontrol.GresLayout{resource},
	})
	if err != nil {
		t.Fatalf("splitGRESResources() error = %v", err)
	}
	if len(indexedGRESResources.Gres) != 0 {
		t.Fatalf("indexed GRES resources = %#v, want none", indexedGRESResources.Gres)
	}
	if !slices.Equal(remainingResources.Gres, []slurmcontrol.GresLayout{resource}) {
		t.Fatalf("remaining resources = %#v, want %#v", remainingResources.Gres, []slurmcontrol.GresLayout{resource})
	}
}
