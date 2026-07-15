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

func TestBuildGRESInventory(t *testing.T) {
	profile, _ := DefaultRegistry().LookupByName("gpu.example.com:gpu-example")
	devices := []DeviceIdentity{
		deviceIDForTest("gpu.example.com", "pool-a", "gpu-0"),
		deviceIDForTest("gpu.example.com", "pool-a", "gpu-1"),
	}

	got, err := BuildGRESInventory(NodeInventory{
		NodeName: "node-a",
		Profiles: []ProfileInventory{{Profile: profile, Devices: devices}},
	})
	if err != nil {
		t.Fatalf("BuildGRESInventory() error = %v", err)
	}
	want := []GRESInventory{{
		ProfileID: "gpu.example.com:gpu-example",
		Name:      "gpu",
		Type:      "gpu-example",
		Devices:   devices,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildGRESInventory() = %#v, want %#v", got, want)
	}

	got[0].Devices[0] = deviceIDForTest("changed.example.com", "pool", "device")
	if reflect.DeepEqual(got[0].Devices, devices) {
		t.Fatal("BuildGRESInventory() reused the input device slice")
	}
}

func TestBuildGRESInventoryEmpty(t *testing.T) {
	got, err := BuildGRESInventory(NodeInventory{})
	if err != nil {
		t.Fatalf("BuildGRESInventory() error = %v", err)
	}
	if got != nil {
		t.Fatalf("BuildGRESInventory() = %#v, want nil", got)
	}
}

func TestBuildGRESInventoryOmitsCoreBitmap(t *testing.T) {
	got, err := BuildGRESInventory(NodeInventory{Profiles: []ProfileInventory{{
		Profile: DeviceProfile{Name: "cpu", Driver: "dra.cpu", Backend: CoreBitmapBackend{}},
	}}})
	if err != nil {
		t.Fatalf("BuildGRESInventory() error = %v", err)
	}
	if got != nil {
		t.Fatalf("BuildGRESInventory() = %#v, want nil", got)
	}
}

func TestBuildGRESInventoryRejectsInvalidProfiles(t *testing.T) {
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
			name: "unsupported backend",
			profiles: []ProfileInventory{{Profile: DeviceProfile{
				Name: "gpu", Driver: "gpu.example.com", Backend: unsupportedBackend{},
			}}},
			wantErr: "unsupported backend",
		},
		{
			name: "duplicate GRES",
			profiles: []ProfileInventory{
				{Profile: DeviceProfile{Name: "gpu", Driver: "driver-a", Backend: IndexedGRESBackend{GRESName: "gpu"}}},
				{Profile: DeviceProfile{Name: "gpu", Driver: "driver-b", Backend: IndexedGRESBackend{GRESName: "gpu"}}},
			},
			wantErr: "map to the same Slurm GRES",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildGRESInventory(NodeInventory{Profiles: tt.profiles})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("BuildGRESInventory() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
