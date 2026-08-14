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
	byName     map[string]DeviceProfile
	bySelector map[string]DeviceProfile
}

// DefaultRegistry returns a registry containing the profiles currently
// supported by slurm-bridge.
func DefaultRegistry() *Registry {
	// Upstream DeviceClass:
	// https://github.com/kubernetes-sigs/dra-example-driver/blob/v0.4.0/deployments/helm/dra-example-driver/templates/deviceclass.yaml
	profile := DeviceProfile{
		Name:     "gpu-example",
		Driver:   "gpu.example.com",
		Selector: `device.driver == "gpu.example.com"`,
		Backend: IndexedGRESBackend{
			GRESName: "gpu",
		},
	}
	return &Registry{
		byName: map[string]DeviceProfile{
			profile.Name: profile,
		},
		bySelector: map[string]DeviceProfile{
			profile.Selector: profile,
		},
	}
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

// SupportsDriver reports whether the registry contains a profile for driver.
func (r *Registry) SupportsDriver(driver string) bool {
	if r == nil {
		return false
	}
	for _, profile := range r.byName {
		if profile.Driver == driver {
			return true
		}
	}
	return false
}

// profilesForDriver returns profiles for driver ordered by profile name.
func (r *Registry) profilesForDriver(driver string) []DeviceProfile {
	profiles := make([]DeviceProfile, 0)
	for _, profile := range r.byName {
		if profile.Driver == driver {
			profiles = append(profiles, profile)
		}
	}
	slices.SortFunc(profiles, func(a, b DeviceProfile) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return profiles
}

// MatchDeviceClass validates a DeviceClass and returns its matching profile.
func (r *Registry) MatchDeviceClass(deviceClass *resourcev1.DeviceClass) (DeviceProfile, error) {
	if deviceClass == nil {
		return DeviceProfile{}, fmt.Errorf("device class must not be nil")
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
