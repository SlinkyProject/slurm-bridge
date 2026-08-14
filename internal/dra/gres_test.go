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

func TestGRESInventorySlurmConfig(t *testing.T) {
	inventory := GRESInventory{
		GRES: GRES{Name: "gpu", Type: "gpu-example"},
		Devices: []DeviceIdentity{
			deviceIDForTest("gpu.example.com", "pool-a", "gpu-0"),
			deviceIDForTest("gpu.example.com", "pool-a", "gpu-1"),
		},
	}

	gres, gresConf, err := inventory.SlurmConfig()
	if err != nil {
		t.Fatalf("GRESInventory.SlurmConfig() error = %v", err)
	}
	if want := "gpu:gpu-example:2"; gres != want {
		t.Fatalf("GRESInventory.SlurmConfig() Gres = %q, want %q", gres, want)
	}
	wantConf := "count=1,name=gpu,type=gpu-example,file=/dra/gpu.example.com/pool-a/gpu-0+" +
		"count=1,name=gpu,type=gpu-example,file=/dra/gpu.example.com/pool-a/gpu-1"
	if gresConf != wantConf {
		t.Fatalf("GRESInventory.SlurmConfig() GresConf = %q, want %q", gresConf, wantConf)
	}
}

func TestAppliedInventoryRoundTrip(t *testing.T) {
	inventory := []GRESInventory{
		{
			GRES: GRES{Name: "gpu", Type: "gpu-example"},
			Devices: []DeviceIdentity{
				deviceIDForTest("gpu.example.com", "rack/pool-a", "gpu-0"),
				deviceIDForTest("gpu.example.com", "rack/pool-a", "gpu-1"),
			},
		},
	}

	extra, err := EncodeAppliedInventory(inventory)
	if err != nil {
		t.Fatalf("EncodeAppliedInventory() error = %v", err)
	}
	wantExtra := `slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu-example":["/dra/gpu.example.com/rack/pool-a/gpu-0","/dra/gpu.example.com/rack/pool-a/gpu-1"]}}`
	if extra != wantExtra {
		t.Fatalf("EncodeAppliedInventory() = %q, want %q", extra, wantExtra)
	}

	got, err := DecodeAppliedInventory(extra)
	if err != nil {
		t.Fatalf("DecodeAppliedInventory() error = %v", err)
	}
	want := AppliedInventory{
		"gpu-example": {
			deviceIDForTest("gpu.example.com", "rack/pool-a", "gpu-0"),
			deviceIDForTest("gpu.example.com", "rack/pool-a", "gpu-1"),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DecodeAppliedInventory() = %#v, want %#v", got, want)
	}
}

func TestEncodeAppliedInventoryEmpty(t *testing.T) {
	got, err := EncodeAppliedInventory(nil)
	if err != nil {
		t.Fatalf("EncodeAppliedInventory() error = %v", err)
	}
	want := `slurm-bridge.dra-gres-map={"v":1,"profiles":{}}`
	if got != want {
		t.Fatalf("EncodeAppliedInventory() = %q, want %q", got, want)
	}
}

func TestEncodeAppliedInventoryRejectsInvalidInventory(t *testing.T) {
	tests := []struct {
		name      string
		inventory []GRESInventory
		wantErr   string
	}{
		{
			name:      "empty profile",
			inventory: []GRESInventory{{GRES: GRES{Name: "gpu"}}},
			wantErr:   "empty device profile name",
		},
		{
			name: "duplicate profile",
			inventory: []GRESInventory{
				{GRES: GRES{Type: "gpu-example"}},
				{GRES: GRES{Type: "gpu-example"}},
			},
			wantErr: "duplicate device profile",
		},
		{
			name: "incomplete identity",
			inventory: []GRESInventory{{
				GRES:    GRES{Type: "gpu-example"},
				Devices: []DeviceIdentity{deviceIDForTest("gpu.example.com", "", "gpu-0")},
			}},
			wantErr: "must contain a driver, pool, and device name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EncodeAppliedInventory(tt.inventory)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("EncodeAppliedInventory() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeAppliedInventoryRejectsInvalidExtra(t *testing.T) {
	tests := []struct {
		name    string
		extra   string
		wantErr string
	}{
		{name: "wrong prefix", extra: `{}`, wantErr: "does not contain a DRA GRES map"},
		{name: "invalid JSON", extra: AppliedInventoryExtraPrefix + `{`, wantErr: "decode applied inventory"},
		{name: "unknown version", extra: AppliedInventoryExtraPrefix + `{"v":2,"profiles":{}}`, wantErr: "unsupported applied inventory version 2"},
		{name: "missing profiles", extra: AppliedInventoryExtraPrefix + `{"v":1}`, wantErr: "has no profiles map"},
		{name: "empty profile", extra: AppliedInventoryExtraPrefix + `{"v":1,"profiles":{"":[]}}`, wantErr: "empty device profile name"},
		{name: "invalid path prefix", extra: AppliedInventoryExtraPrefix + `{"v":1,"profiles":{"gpu-example":["gpu.example.com/pool/gpu-0"]}}`, wantErr: `must start with "/dra/"`},
		{name: "incomplete path", extra: AppliedInventoryExtraPrefix + `{"v":1,"profiles":{"gpu-example":["/dra/gpu.example.com/gpu-0"]}}`, wantErr: "must contain a driver, pool, and device name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeAppliedInventory(tt.extra)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("DecodeAppliedInventory() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
