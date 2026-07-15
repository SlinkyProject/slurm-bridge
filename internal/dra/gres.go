// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"fmt"
	"slices"
)

// GRESInventory describes one indexed Slurm GRES and its stable DRA device
// mapping. Devices[i] is the DRA device represented by Slurm index i.
type GRESInventory struct {
	ProfileID string
	Name      string
	Type      string
	Devices   []DeviceIdentity
}

// BuildGRESInventory translates the indexed-GRES profiles in a NodeInventory
// into Slurm GRES inventory. Non-GRES profiles are omitted.
func BuildGRESInventory(nodeInventory NodeInventory) ([]GRESInventory, error) {
	type gresID struct {
		name     string
		typeName string
	}

	seen := make(map[gresID]string)
	var inventory []GRESInventory
	for _, profileInventory := range nodeInventory.Profiles {
		profile := profileInventory.Profile
		switch backend := profile.Backend.(type) {
		case IndexedGRESBackend:
			if backend.GRESName == "" {
				return nil, fmt.Errorf("device profile %q has an empty Slurm GRES name", profile.ID())
			}
			if profile.Name == "" {
				return nil, fmt.Errorf("device profile for driver %q has an empty name", profile.Driver)
			}
			id := gresID{name: backend.GRESName, typeName: profile.Name}
			if existing, ok := seen[id]; ok {
				return nil, fmt.Errorf("device profiles %q and %q map to the same Slurm GRES %q", existing, profile.ID(), backend.GRESName+":"+profile.Name)
			}
			seen[id] = profile.ID()
			inventory = append(inventory, GRESInventory{
				ProfileID: profile.ID(),
				Name:      backend.GRESName,
				Type:      profile.Name,
				Devices:   slices.Clone(profileInventory.Devices),
			})
		case CoreBitmapBackend:
			continue
		default:
			return nil, fmt.Errorf("device profile %q has unsupported backend %T", profile.ID(), profile.Backend)
		}
	}
	return inventory, nil
}
