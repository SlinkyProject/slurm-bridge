// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"testing"
)

func TestBackendString(t *testing.T) {
	tests := []struct {
		backend Backend
		want    string
	}{
		{backend: CoreBitmapBackend{}, want: "core-bitmap"},
		{backend: IndexedGRESBackend{GRESName: "gpu"}, want: "indexed-gres"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.backend.String(); got != tt.want {
				t.Fatalf("Backend.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeviceProfileBackendCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		profile     DeviceProfile
		coreBitmap  bool
		indexedGRES bool
	}{
		{name: "no backend", profile: DeviceProfile{}},
		{name: "core bitmap", profile: DeviceProfile{Backend: CoreBitmapBackend{}}, coreBitmap: true},
		{name: "indexed GRES", profile: DeviceProfile{Backend: IndexedGRESBackend{GRESName: "gpu"}}, indexedGRES: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.profile.UsesCoreBitmap(); got != tt.coreBitmap {
				t.Errorf("DeviceProfile.UsesCoreBitmap() = %t, want %t", got, tt.coreBitmap)
			}
			if got := tt.profile.UsesIndexedGRES(); got != tt.indexedGRES {
				t.Errorf("DeviceProfile.UsesIndexedGRES() = %t, want %t", got, tt.indexedGRES)
			}
		})
	}
}
