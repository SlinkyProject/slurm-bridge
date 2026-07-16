// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"slices"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

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

	profileResources, legacyResources, err := splitGRESResources(resources)
	if err != nil {
		t.Fatalf("splitGRESResources() error = %v", err)
	}
	wantProfile := []slurmcontrol.GresLayout{
		{Name: "gpu", Type: "gpu-example", Count: 2, Index: "0-1"},
		{Name: "gpu", Type: "gpu-nvidia", Count: 1, Index: "1"},
	}
	wantLegacy := []slurmcontrol.GresLayout{
		{Name: "gpu", Type: "gpu.example.com", Count: 1, Index: "2"},
		{Name: "gpu", Type: "gpu.nvidia.com", Count: 1, Index: "3"},
		{Name: "license", Type: "matlab", Count: 1},
	}
	if !slices.Equal(profileResources.Gres, wantProfile) {
		t.Errorf("profile resources = %#v, want %#v", profileResources.Gres, wantProfile)
	}
	if !slices.Equal(legacyResources.Gres, wantLegacy) {
		t.Errorf("legacy resources = %#v, want %#v", legacyResources.Gres, wantLegacy)
	}
	if profileResources.Node != resources.Node || legacyResources.Node != resources.Node ||
		profileResources.NodeExtra != resources.NodeExtra || legacyResources.NodeExtra != resources.NodeExtra {
		t.Fatalf("split resources did not preserve node metadata: profile=%#v legacy=%#v", profileResources, legacyResources)
	}
	if len(resources.Gres) != 5 {
		t.Fatalf("splitGRESResources() mutated its input: %#v", resources.Gres)
	}
}

func TestSplitGRESResourcesRejectsWrongProfileGRESName(t *testing.T) {
	_, _, err := splitGRESResources(slurmcontrol.NodeResources{
		Gres: []slurmcontrol.GresLayout{{Name: "accelerator", Type: "gpu-example", Count: 1, Index: "0"}},
	})
	if err == nil {
		t.Fatal("splitGRESResources() error = nil, want profile GRES name mismatch")
	}
}
