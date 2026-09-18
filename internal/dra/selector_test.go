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
			name:     "matching singleton driver membership",
			selector: `device.driver in ['driver-a.example.com']`,
		},
		{
			name:     "different singleton driver membership",
			selector: `device.driver in ['driver-b.example.com']`,
			wantErr:  `constrains device.driver to "driver-b.example.com", but configured driver is "driver-a.example.com"`,
		},
		{
			name:     "different singleton driver membership with attributes",
			selector: `device.attributes['driver-a.example.com'].model == 'a' && device.driver in ["driver-b.example.com"]`,
			wantErr:  `constrains device.driver to "driver-b.example.com", but configured driver is "driver-a.example.com"`,
		},
		{
			name:     "multiple driver membership",
			selector: `device.driver in ['driver-b.example.com', 'driver-a.example.com']`,
		},
		{
			name:     "empty driver membership",
			selector: `device.driver in []`,
		},
		{
			name:     "computed singleton driver membership",
			selector: `device.driver in [device.attributes['driver-a.example.com'].model]`,
		},
		{
			name:     "non-driver singleton membership",
			selector: `device.attributes['driver-a.example.com'].model in ['driver-b.example.com']`,
		},
		{
			name:     "negated singleton driver membership",
			selector: `!(device.driver in ['driver-b.example.com'])`,
		},
		{
			name:     "alternative singleton driver memberships",
			selector: `device.driver in ['driver-a.example.com'] || device.driver in ['driver-b.example.com']`,
		},
		{
			name:     "negated driver equality",
			selector: `!(device.driver == 'driver-b.example.com')`,
		},
		{
			name:     "alternative driver equalities",
			selector: `device.driver == 'driver-a.example.com' || device.driver == 'driver-b.example.com'`,
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
