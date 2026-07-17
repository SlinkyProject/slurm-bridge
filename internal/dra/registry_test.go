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

type registryUnsupportedBackend struct{}

func (registryUnsupportedBackend) String() string {
	return "unsupported"
}

func TestDefaultRegistry(t *testing.T) {
	wants := []DeviceProfile{
		{
			Name:     "cpu",
			Driver:   "dra.cpu",
			Selector: `device.driver == "dra.cpu"`,
			Backend:  CoreBitmapBackend{},
		},
		{
			Name:     "gpu-example",
			Driver:   "gpu.example.com",
			Selector: `device.driver == 'gpu.example.com'`,
			Backend:  IndexedGRESBackend{GRESName: "gpu"},
		},
		{
			Name:     "gpu-nvidia",
			Driver:   "gpu.nvidia.com",
			Selector: `device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'`,
			Backend:  IndexedGRESBackend{GRESName: "gpu"},
		},
	}
	registry := DefaultRegistry()

	for _, want := range wants {
		if got, ok := registry.LookupByName(want.Name); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("Registry.LookupByName() = (%#v, %t), want (%#v, true)", got, ok, want)
		}
		if got, ok := registry.LookupBySelector(want.Selector); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("Registry.LookupBySelector() = (%#v, %t), want (%#v, true)", got, ok, want)
		}
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

func TestRegistryMatchIndexedGRES(t *testing.T) {
	registry := DefaultRegistry()
	tests := []struct {
		name      string
		gres      GRES
		wantName  string
		wantOwned bool
		wantErr   string
	}{
		{
			name:      "indexed GRES",
			gres:      GRES{Name: "gpu", Type: "gpu-example"},
			wantName:  "gpu-example",
			wantOwned: true,
		},
		{
			name: "unknown type",
			gres: GRES{Name: "license", Type: "matlab"},
		},
		{
			name: "core-bitmap profile name",
			gres: GRES{Name: "gpu", Type: "cpu"},
		},
		{
			name:      "wrong GRES name",
			gres:      GRES{Name: "accelerator", Type: "gpu-example"},
			wantOwned: true,
			wantErr:   `expected "gpu" for DeviceProfile "gpu-example"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, owned, err := registry.MatchIndexedGRES(tt.gres)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Registry.MatchIndexedGRES() error = %v, want error containing %q", err, tt.wantErr)
				}
				if owned != tt.wantOwned {
					t.Fatalf("Registry.MatchIndexedGRES() owned = %t, want %t", owned, tt.wantOwned)
				}
				return
			}
			if err != nil {
				t.Fatalf("Registry.MatchIndexedGRES() error = %v", err)
			}
			if owned != tt.wantOwned || profile.Name != tt.wantName {
				t.Fatalf("Registry.MatchIndexedGRES() = (%q, %t), want (%q, %t)", profile.Name, owned, tt.wantName, tt.wantOwned)
			}
		})
	}
}

func TestNewRegistryRejectsDuplicateKeys(t *testing.T) {
	profile := DeviceProfile{
		Name:     "profile-a",
		Driver:   "driver-a",
		Selector: `device.driver == 'driver-a'`,
		Backend:  IndexedGRESBackend{GRESName: "gpu"},
	}

	t.Run("name", func(t *testing.T) {
		duplicate := profile
		duplicate.Driver = "driver-b"
		duplicate.Selector = `device.driver == 'driver-b'`
		if _, err := newRegistry(profile, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate name") {
			t.Fatalf("newRegistry() error = %v, want duplicate name error", err)
		}
	})

	t.Run("selector", func(t *testing.T) {
		duplicate := profile
		duplicate.Name = "profile-b"
		duplicate.Driver = "driver-b"
		if _, err := newRegistry(profile, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate selector") {
			t.Fatalf("newRegistry() error = %v, want duplicate selector error", err)
		}
	})
}

func TestNewRegistryRejectsInvalidProfiles(t *testing.T) {
	valid := DeviceProfile{
		Name:     "profile-a",
		Driver:   "driver-a.example.com",
		Selector: `device.driver == 'driver-a.example.com'`,
		Backend:  IndexedGRESBackend{GRESName: "gpu"},
	}
	tests := []struct {
		name    string
		mutate  func(*DeviceProfile)
		wantErr string
	}{
		{
			name:    "empty name",
			mutate:  func(profile *DeviceProfile) { profile.Name = "" },
			wantErr: "empty name",
		},
		{
			name:    "empty driver",
			mutate:  func(profile *DeviceProfile) { profile.Driver = "" },
			wantErr: "empty driver",
		},
		{
			name:    "empty selector",
			mutate:  func(profile *DeviceProfile) { profile.Selector = "" },
			wantErr: "empty selector",
		},
		{
			name:    "nil backend",
			mutate:  func(profile *DeviceProfile) { profile.Backend = nil },
			wantErr: "unsupported backend",
		},
		{
			name:    "unsupported backend",
			mutate:  func(profile *DeviceProfile) { profile.Backend = registryUnsupportedBackend{} },
			wantErr: "unsupported backend",
		},
		{
			name:    "empty GRES name",
			mutate:  func(profile *DeviceProfile) { profile.Backend = IndexedGRESBackend{} },
			wantErr: "empty Slurm GRES name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := valid
			tt.mutate(&profile)
			if _, err := newRegistry(profile); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("newRegistry() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}

	t.Run("multiple core-bitmap profiles", func(t *testing.T) {
		profileA := valid
		profileA.Backend = CoreBitmapBackend{}
		profileB := DeviceProfile{
			Name:     "profile-b",
			Driver:   "driver-b.example.com",
			Selector: `device.driver == 'driver-b.example.com'`,
			Backend:  CoreBitmapBackend{},
		}
		if _, err := newRegistry(profileA, profileB); err == nil || !strings.Contains(err.Error(), "both use the core-bitmap backend") {
			t.Fatalf("newRegistry() error = %v, want multiple core-bitmap profiles error", err)
		}
	})
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
	nvidia, _ := registry.LookupByName("gpu-nvidia")
	if got := registry.profilesForDriver("gpu.nvidia.com"); !reflect.DeepEqual(got, []DeviceProfile{nvidia}) {
		t.Fatalf("Registry.profilesForDriver() = %#v, want %#v", got, []DeviceProfile{nvidia})
	}
	if !registry.SupportsDriver("gpu.nvidia.com") {
		t.Fatal("Registry.SupportsDriver() = false for the NVIDIA driver")
	}
	cpu, _ := registry.LookupByName("cpu")
	if got := registry.profilesForDriver("dra.cpu"); !reflect.DeepEqual(got, []DeviceProfile{cpu}) {
		t.Fatalf("Registry.profilesForDriver() = %#v, want %#v", got, []DeviceProfile{cpu})
	}
	if !registry.SupportsDriver("dra.cpu") {
		t.Fatal("Registry.SupportsDriver() = false for the CPU driver")
	}
	profileB := DeviceProfile{
		Name:     "profile-b",
		Driver:   "shared.example.com",
		Selector: `device.driver == 'shared.example.com' && device.attributes['shared.example.com'].model == 'b'`,
		Backend:  IndexedGRESBackend{GRESName: "gpu"},
	}
	profileA := DeviceProfile{
		Name:     "profile-a",
		Driver:   "shared.example.com",
		Selector: `device.driver == 'shared.example.com' && device.attributes['shared.example.com'].model == 'a'`,
		Backend:  IndexedGRESBackend{GRESName: "gpu"},
	}
	registry, err := newRegistry(profileB, profileA)
	if err != nil {
		t.Fatalf("newRegistry() error = %v", err)
	}
	if got := registry.profilesForDriver("shared.example.com"); !reflect.DeepEqual(got, []DeviceProfile{profileA, profileB}) {
		t.Fatalf("Registry.profilesForDriver() = %#v, want profiles ordered by name", got)
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

	t.Run("matching NVIDIA GPU class", func(t *testing.T) {
		deviceClass := valid()
		deviceClass.Name = "gpu.nvidia.com"
		deviceClass.Spec.Selectors[0].CEL.Expression = `device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'`

		got, err := registry.MatchDeviceClass(deviceClass)
		if err != nil {
			t.Fatalf("Registry.MatchDeviceClass() error = %v", err)
		}
		want, _ := registry.LookupByName("gpu-nvidia")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Registry.MatchDeviceClass() = %#v, want %#v", got, want)
		}
	})

	t.Run("matching CPU class", func(t *testing.T) {
		deviceClass := valid()
		deviceClass.Name = "dra.cpu"
		deviceClass.Spec.Selectors[0].CEL.Expression = `device.driver == "dra.cpu"`

		got, err := registry.MatchDeviceClass(deviceClass)
		if err != nil {
			t.Fatalf("Registry.MatchDeviceClass() error = %v", err)
		}
		want, _ := registry.LookupByName("cpu")
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
			name: "configuration",
			class: func() *resourcev1.DeviceClass {
				class := valid()
				class.Spec.Config = []resourcev1.DeviceClassConfiguration{{}}
				return class
			},
			wantErr: "configuration is not supported",
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
