// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"cmp"
	"fmt"
	"slices"

	resourcev1 "k8s.io/api/resource/v1"
)

// Registry indexes supported DeviceProfiles by name and canonical selector.
type Registry struct {
	byName            map[string]DeviceProfile
	bySelector        map[string]DeviceProfile
	byDriver          map[string][]DeviceProfile
	byIndexedGRESType map[string]DeviceProfile
}

func newRegistry(profiles ...DeviceProfile) (*Registry, error) {
	registry := &Registry{
		byName:            make(map[string]DeviceProfile, len(profiles)),
		bySelector:        make(map[string]DeviceProfile, len(profiles)),
		byDriver:          make(map[string][]DeviceProfile),
		byIndexedGRESType: make(map[string]DeviceProfile),
	}
	coreBitmapProfile := ""
	for _, profile := range profiles {
		if profile.Name == "" {
			return nil, fmt.Errorf("device profile for driver %q has an empty name", profile.Driver)
		}
		if profile.Driver == "" {
			return nil, fmt.Errorf("device profile %q has an empty driver", profile.Name)
		}
		if profile.Selector == "" {
			return nil, fmt.Errorf("device profile %q has an empty selector", profile.Name)
		}
		if existing, ok := registry.byName[profile.Name]; ok {
			return nil, fmt.Errorf("device profiles %q and %q have duplicate name %q", existing.Driver, profile.Driver, profile.Name)
		}
		if existing, ok := registry.bySelector[profile.Selector]; ok {
			return nil, fmt.Errorf("device profiles %q and %q have duplicate selector %q", existing.Name, profile.Name, profile.Selector)
		}

		switch backend := profile.Backend.(type) {
		case CoreBitmapBackend:
			if coreBitmapProfile != "" {
				return nil, fmt.Errorf("device profiles %q and %q both use the core-bitmap backend", coreBitmapProfile, profile.Name)
			}
			coreBitmapProfile = profile.Name
		case IndexedGRESBackend:
			if backend.GRESName == "" {
				return nil, fmt.Errorf("device profile %q has an empty Slurm GRES name", profile.Name)
			}
			registry.byIndexedGRESType[profile.Name] = profile
		default:
			return nil, fmt.Errorf("device profile %q has unsupported backend %T", profile.Name, profile.Backend)
		}

		registry.byName[profile.Name] = profile
		registry.bySelector[profile.Selector] = profile
		registry.byDriver[profile.Driver] = append(registry.byDriver[profile.Driver], profile)
	}
	for driver := range registry.byDriver {
		slices.SortFunc(registry.byDriver[driver], func(a, b DeviceProfile) int {
			return cmp.Compare(a.Name, b.Name)
		})
	}
	return registry, nil
}

func mustNewRegistry(profiles ...DeviceProfile) *Registry {
	registry, err := newRegistry(profiles...)
	if err != nil {
		panic(err)
	}
	return registry
}

// DefaultRegistry returns a registry containing the profiles currently
// supported by slurm-bridge.
func DefaultRegistry() *Registry {
	// Upstream DeviceClass:
	// https://github.com/kubernetes-sigs/dra-driver-cpu/blob/main/deployment/helm/dra-driver-cpu/templates/deviceclass.yaml
	cpu := DeviceProfile{
		Name:     "cpu",
		Driver:   "dra.cpu",
		Selector: `device.driver == "dra.cpu"`,
		Backend:  CoreBitmapBackend{},
	}
	// Upstream DeviceClass:
	// https://github.com/kubernetes-sigs/dra-example-driver/blob/v0.4.0/deployments/helm/dra-example-driver/templates/deviceclass.yaml
	exampleGPU := DeviceProfile{
		Name:     "gpu-example",
		Driver:   "gpu.example.com",
		Selector: `device.driver == 'gpu.example.com'`,
		Backend: IndexedGRESBackend{
			GRESName: "gpu",
		},
	}
	// Upstream DeviceClass:
	// https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/blob/v0.4.0/deployments/helm/dra-driver-nvidia-gpu/templates/deviceclass-gpu.yaml
	nvidiaGPU := DeviceProfile{
		Name:     "gpu-nvidia",
		Driver:   "gpu.nvidia.com",
		Selector: `device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'`,
		Backend: IndexedGRESBackend{
			GRESName: "gpu",
		},
	}
	return mustNewRegistry(cpu, exampleGPU, nvidiaGPU)
}

// LookupByName returns the profile with the given stable profile name.
func (r *Registry) LookupByName(name string) (DeviceProfile, bool) {
	profile, ok := r.byName[name]
	return profile, ok
}

// LookupBySelector returns the profile with the exact canonical CEL selector.
// Selector matching is deliberately byte-for-byte.
func (r *Registry) LookupBySelector(selector string) (DeviceProfile, bool) {
	profile, ok := r.bySelector[selector]
	return profile, ok
}

// MatchIndexedGRES returns the profile which owns gres. Profiles using other
// backends do not claim the Slurm GRES type namespace. A known indexed-GRES
// type with the wrong GRES name is rejected.
func (r *Registry) MatchIndexedGRES(gres GRES) (DeviceProfile, bool, error) {
	profile, owned := r.byIndexedGRESType[gres.Type]
	if !owned {
		return DeviceProfile{}, false, nil
	}
	expected, err := profile.GRES()
	if err != nil {
		return DeviceProfile{}, true, err
	}
	if gres.Name != expected.Name {
		return DeviceProfile{}, true, fmt.Errorf(
			"Slurm GRES type %q uses name %q, expected %q for DeviceProfile %q",
			gres.Type, gres.Name, expected.Name, profile.Name,
		)
	}
	return profile, true, nil
}

// SupportsDriver reports whether the registry contains a profile for driver.
func (r *Registry) SupportsDriver(driver string) bool {
	_, ok := r.byDriver[driver]
	return ok
}

// profilesForDriver returns profiles for driver ordered by profile name.
func (r *Registry) profilesForDriver(driver string) []DeviceProfile {
	return slices.Clone(r.byDriver[driver])
}

// MatchDeviceClass validates a DeviceClass and returns its matching profile.
func (r *Registry) MatchDeviceClass(deviceClass *resourcev1.DeviceClass) (DeviceProfile, error) {
	if deviceClass == nil {
		return DeviceProfile{}, fmt.Errorf("device class must not be nil")
	}
	if len(deviceClass.Spec.Config) != 0 {
		return DeviceProfile{}, fmt.Errorf("device class %q configuration is not supported", deviceClass.Name)
	}
	if len(deviceClass.Spec.Selectors) != 1 {
		return DeviceProfile{}, fmt.Errorf("device class %q must have exactly one selector", deviceClass.Name)
	}
	selector := deviceClass.Spec.Selectors[0]
	if selector.CEL == nil {
		return DeviceProfile{}, fmt.Errorf("device class %q selector must be a CEL selector", deviceClass.Name)
	}
	profile, ok := r.LookupBySelector(selector.CEL.Expression)
	if !ok {
		return DeviceProfile{}, fmt.Errorf("device class %q selector does not match a supported device profile", deviceClass.Name)
	}
	return profile, nil
}
