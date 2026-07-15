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

func TestDeviceProfileID(t *testing.T) {
	profile := DeviceProfile{Driver: "gpu.example.com", Name: "gpu-example"}
	if got, want := profile.ID(), "gpu.example.com:gpu-example"; got != want {
		t.Fatalf("DeviceProfile.ID() = %q, want %q", got, want)
	}
}
