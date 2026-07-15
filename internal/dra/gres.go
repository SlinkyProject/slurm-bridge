// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"fmt"
	"slices"
)

// GRES identifies a Slurm generic resource by name and type.
type GRES struct {
	Name string
	Type string
}

// String returns the Slurm name:type form.
func (g GRES) String() string {
	return g.Name + ":" + g.Type
}

// GRESInventory describes one indexed Slurm GRES and its stable DRA device
// mapping. Devices[i] is the DRA device represented by Slurm index i.
type GRESInventory struct {
	GRES    GRES
	Devices []DeviceIdentity
}

// GRES translates indexed-GRES profiles into Slurm GRES inventory.
func (n NodeInventory) GRES() ([]GRESInventory, error) {
	var inventory []GRESInventory
	for _, profileInventory := range n.Profiles {
		profile := profileInventory.Profile
		switch backend := profile.Backend.(type) {
		case IndexedGRESBackend:
			if backend.GRESName == "" {
				return nil, fmt.Errorf("device profile %q has an empty Slurm GRES name", profile.Name)
			}
			if profile.Name == "" {
				return nil, fmt.Errorf("device profile for driver %q has an empty name", profile.Driver)
			}
			inventory = append(inventory, GRESInventory{
				GRES: GRES{
					Name: backend.GRESName,
					Type: profile.Name,
				},
				Devices: slices.Clone(profileInventory.Devices),
			})
		default:
			return nil, fmt.Errorf("device profile %q has unsupported backend %T", profile.Name, profile.Backend)
		}
	}
	return inventory, nil
}
