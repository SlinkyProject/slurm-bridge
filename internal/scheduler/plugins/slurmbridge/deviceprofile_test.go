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
