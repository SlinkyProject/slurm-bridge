// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"reflect"
	"strings"
	"testing"
)

type unsupportedBackend struct{}

func (unsupportedBackend) String() string { return "unsupported" }

func TestNodeInventoryGRES(t *testing.T) {
	profile, _ := DefaultRegistry().LookupByName("gpu-example")
	devices := []DeviceIdentity{
		deviceIDForTest("gpu.example.com", "pool-a", "gpu-0"),
		deviceIDForTest("gpu.example.com", "pool-a", "gpu-1"),
	}

	got, err := (NodeInventory{
		NodeName: "node-a",
		Profiles: []ProfileInventory{{Profile: profile, Devices: devices}},
	}).GRES()
	if err != nil {
		t.Fatalf("NodeInventory.GRES() error = %v", err)
	}
	want := []GRESInventory{{
		GRES:    GRES{Name: "gpu", Type: "gpu-example"},
		Devices: devices,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeInventory.GRES() = %#v, want %#v", got, want)
	}
	if got, want := got[0].GRES.String(), "gpu:gpu-example"; got != want {
		t.Fatalf("GRES.String() = %q, want %q", got, want)
	}

	got[0].Devices[0] = deviceIDForTest("changed.example.com", "pool", "device")
	if reflect.DeepEqual(got[0].Devices, devices) {
		t.Fatal("NodeInventory.GRES() reused the input device slice")
	}
}

func TestNodeInventoryGRESEmpty(t *testing.T) {
	got, err := (NodeInventory{}).GRES()
	if err != nil {
		t.Fatalf("NodeInventory.GRES() error = %v", err)
	}
	if got != nil {
		t.Fatalf("NodeInventory.GRES() = %#v, want nil", got)
	}
}

func TestNodeInventoryGRESRejectsInvalidProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profiles []ProfileInventory
		wantErr  string
	}{
		{
			name: "empty GRES name",
			profiles: []ProfileInventory{{Profile: DeviceProfile{
				Name: "gpu", Driver: "gpu.example.com", Backend: IndexedGRESBackend{},
			}}},
			wantErr: "empty Slurm GRES name",
		},
		{
			name: "empty profile name",
			profiles: []ProfileInventory{{Profile: DeviceProfile{
				Driver: "gpu.example.com", Backend: IndexedGRESBackend{GRESName: "gpu"},
			}}},
			wantErr: "empty name",
		},
		{
			name: "unsupported backend",
			profiles: []ProfileInventory{{Profile: DeviceProfile{
				Name: "gpu", Driver: "gpu.example.com", Backend: unsupportedBackend{},
			}}},
			wantErr: "unsupported backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (NodeInventory{Profiles: tt.profiles}).GRES()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NodeInventory.GRES() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
