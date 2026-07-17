// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"slices"
	"strings"
	"testing"

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
		Name: "gpu", Type: "gpu-example", Count: 3, Index: "3,1,0",
	}})
	if err != nil {
		t.Fatalf("allocateIndexedGRESProfiles() error = %v", err)
	}
	if len(allocations) != 2 || !slices.Equal(allocations[0].Indexes, []int{3}) || !slices.Equal(allocations[1].Indexes, []int{1, 0}) {
		t.Fatalf("allocateIndexedGRESProfiles() = %#v, want indexes [3] and [1 0]", allocations)
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
