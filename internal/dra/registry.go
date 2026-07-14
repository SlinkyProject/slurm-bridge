// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

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
