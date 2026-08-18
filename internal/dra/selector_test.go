// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"strings"
	"testing"
)

func TestNewRegistryValidatesSelectorDriver(t *testing.T) {
	profile := DeviceProfile{
		Name:    "test-profile",
		Driver:  "driver-a.example.com",
		Backend: IndexedGRESBackend{GRESName: "device"},
	}
	tests := []struct {
		name     string
		selector string
		wantErr  string
	}{
		{
			name:     "matching driver",
			selector: `device.driver == 'driver-a.example.com'`,
		},
		{
			name:     "matching driver with double quotes and attributes",
			selector: `device.driver == "driver-a.example.com" && device.attributes['driver-a.example.com'].model == 'a'`,
		},
		{
			name:     "reversed equality",
			selector: `'driver-a.example.com' == device.driver`,
		},
		{
			name:     "different driver",
			selector: `device.driver == 'driver-b.example.com'`,
			wantErr:  `constrains device.driver to "driver-b.example.com", but configured driver is "driver-a.example.com"`,
		},
		{
			name:     "no direct driver equality",
			selector: `device.attributes['driver-a.example.com'].model == 'a'`,
		},
		{
			name:     "matching driver equality under disjunction",
			selector: `device.driver == 'driver-a.example.com' || device.attributes['driver-a.example.com'].model == 'a'`,
		},
		{
			name:     "additional contradictory driver constraint",
			selector: `device.driver == 'driver-a.example.com' && device.driver == 'driver-b.example.com'`,
			wantErr:  `constrains device.driver to "driver-b.example.com", but configured driver is "driver-a.example.com"`,
		},
		{
			name:     "non-equality driver expression",
			selector: `device.driver == 'driver-a.example.com' && device.driver != 'driver-b.example.com'`,
		},
		{
			name:     "invalid CEL",
			selector: `device.driver ==`,
			wantErr:  "compile selector",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := profile
			profile.Selector = tt.selector
			_, err := NewRegistry([]DeviceProfile{profile})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("NewRegistry() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewRegistry() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
