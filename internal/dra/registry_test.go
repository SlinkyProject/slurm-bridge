// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"reflect"
	"testing"
)

func TestDefaultRegistry(t *testing.T) {
	want := DeviceProfile{
		Name:     "gpu-example",
		Driver:   "gpu.example.com",
		Selector: `device.driver == "gpu.example.com"`,
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
	selector := `device.driver == "gpu.example.com"`

	if _, ok := registry.LookupByName("GPU-example"); ok {
		t.Fatal("Registry.LookupByName() accepted a non-canonical name")
	}
	if _, ok := registry.LookupBySelector(" " + selector); ok {
		t.Fatal("Registry.LookupBySelector() accepted a non-canonical selector")
	}
}
