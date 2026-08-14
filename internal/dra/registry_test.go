// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"reflect"
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDefaultRegistry(t *testing.T) {
	want := DeviceProfile{
		Name:     "gpu-example",
		Driver:   "gpu.example.com",
		Selector: `device.driver == 'gpu.example.com'`,
		Backend:  IndexedGRESBackend{GRESName: "gpu"},
	}
	registry := DefaultRegistry()

	if got, ok := registry.LookupByName(want.Name); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Registry.LookupByName() = (%#v, %t), want (%#v, true)", got, ok, want)
	}
	if got, ok := registry.LookupBySelector(want.Selector); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Registry.LookupBySelector() = (%#v, %t), want (%#v, true)", got, ok, want)
	}
}

func TestRegistryLookupsAreExact(t *testing.T) {
	registry := DefaultRegistry()
	selector := `device.driver == 'gpu.example.com'`

	if _, ok := registry.LookupByName("GPU-example"); ok {
		t.Fatal("Registry.LookupByName() accepted a non-canonical name")
	}
	if _, ok := registry.LookupBySelector(" " + selector); ok {
		t.Fatal("Registry.LookupBySelector() accepted a non-canonical selector")
	}
}

func TestRegistryProfilesForDriver(t *testing.T) {
	registry := DefaultRegistry()
	profile, _ := registry.LookupByName("gpu-example")

	if got := registry.profilesForDriver("gpu.example.com"); !reflect.DeepEqual(got, []DeviceProfile{profile}) {
		t.Fatalf("Registry.profilesForDriver() = %#v, want %#v", got, []DeviceProfile{profile})
	}
	if got := registry.profilesForDriver("unsupported.example.com"); len(got) != 0 {
		t.Fatalf("Registry.profilesForDriver() = %#v, want no profiles", got)
	}
	if !registry.SupportsDriver("gpu.example.com") {
		t.Fatal("Registry.SupportsDriver() = false for the example driver")
	}
	if registry.SupportsDriver("gpu.nvidia.com") {
		t.Fatal("Registry.SupportsDriver() = true for an unregistered driver")
	}
}

func TestRegistryMatchDeviceClass(t *testing.T) {
	registry := DefaultRegistry()
	valid := func() *resourcev1.DeviceClass {
		return &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gpu.example.com",
			},
			Spec: resourcev1.DeviceClassSpec{
				Selectors: []resourcev1.DeviceSelector{{
					CEL: &resourcev1.CELDeviceSelector{
						Expression: `device.driver == 'gpu.example.com'`,
					},
				}},
			},
		}
	}

	t.Run("matching class", func(t *testing.T) {
		got, err := registry.MatchDeviceClass(valid())
		if err != nil {
			t.Fatalf("Registry.MatchDeviceClass() error = %v", err)
		}
		want, _ := registry.LookupByName("gpu-example")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Registry.MatchDeviceClass() = %#v, want %#v", got, want)
		}
	})

	tests := []struct {
		name    string
		class   func() *resourcev1.DeviceClass
		wantErr string
	}{
		{
			name:    "nil class",
			class:   func() *resourcev1.DeviceClass { return nil },
			wantErr: "must not be nil",
		},
		{
			name: "no selectors",
			class: func() *resourcev1.DeviceClass {
				class := valid()
				class.Spec.Selectors = nil
				return class
			},
			wantErr: "must have exactly one selector",
		},
		{
			name: "multiple selectors",
			class: func() *resourcev1.DeviceClass {
				class := valid()
				class.Spec.Selectors = append(class.Spec.Selectors, class.Spec.Selectors[0])
				return class
			},
			wantErr: "must have exactly one selector",
		},
		{
			name: "non-CEL selector",
			class: func() *resourcev1.DeviceClass {
				class := valid()
				class.Spec.Selectors[0].CEL = nil
				return class
			},
			wantErr: "must be a CEL selector",
		},
		{
			name: "non-canonical selector",
			class: func() *resourcev1.DeviceClass {
				class := valid()
				class.Spec.Selectors[0].CEL.Expression = ` device.driver == 'gpu.example.com'`
				return class
			},
			wantErr: "does not match a supported device profile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := registry.MatchDeviceClass(tt.class())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Registry.MatchDeviceClass() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
