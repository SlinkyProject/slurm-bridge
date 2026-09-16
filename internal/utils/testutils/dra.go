// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package testutils

import "github.com/SlinkyProject/slurm-bridge/internal/dra"

// DRARegistryWithExampleGPU explicitly enables the example GPU fixture alongside
// the built-in profiles.
func DRARegistryWithExampleGPU() *dra.Registry {
	defaults := dra.DefaultRegistry()
	profiles := []dra.DeviceProfile{{
		Name:     "gpu-example",
		Driver:   "gpu.example.com",
		Selector: `device.driver == 'gpu.example.com'`,
		Backend:  dra.IndexedGRESBackend{GRESName: "gpu"},
	}}
	for _, name := range []string{"cpu", "gpu-nvidia", "dranet-rdma"} {
		profile, ok := defaults.LookupByName(name)
		if !ok {
			panic("missing built-in device profile: " + name)
		}
		profiles = append(profiles, profile)
	}
	registry, err := dra.NewRegistry(profiles)
	if err != nil {
		panic(err)
	}
	return registry
}
