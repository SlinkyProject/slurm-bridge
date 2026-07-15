// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"k8s.io/dynamic-resource-allocation/structured"
)

const (
	// AppliedInventoryExtraPrefix identifies node Extra values owned by the DRA
	// GRES inventory integration.
	AppliedInventoryExtraPrefix = "slurm-bridge.dra-gres-map="
	// appliedInventoryVersion records the version of the format used for storing DRA device identities in Slurm
	// node Extra fields. This version is required if the format changes in future.
	appliedInventoryVersion = 1
	devicePathPrefix        = "/dra/"
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

// GRES returns the indexed Slurm GRES represented by this DeviceProfile.
func (p DeviceProfile) GRES() (GRES, error) {
	switch backend := p.Backend.(type) {
	case IndexedGRESBackend:
		if backend.GRESName == "" {
			return GRES{}, fmt.Errorf("device profile %q has an empty Slurm GRES name", p.Name)
		}
		if p.Name == "" {
			return GRES{}, fmt.Errorf("device profile for driver %q has an empty name", p.Driver)
		}
		return GRES{Name: backend.GRESName, Type: p.Name}, nil
	default:
		return GRES{}, fmt.Errorf("device profile %q has unsupported backend %T", p.Name, p.Backend)
	}
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
		gres, err := profile.GRES()
		if err != nil {
			return nil, err
		}
		inventory = append(inventory, GRESInventory{
			GRES:    gres,
			Devices: slices.Clone(profileInventory.Devices),
		})
	}
	return inventory, nil
}

func (g GRESInventory) appliedInventoryEntry() (string, []string, error) {
	profileName := g.GRES.Type
	if profileName == "" {
		return "", nil, fmt.Errorf("cannot encode applied inventory with an empty device profile name")
	}
	devices := make([]string, len(g.Devices))
	for i, device := range g.Devices {
		path, err := encodeDevicePath(device)
		if err != nil {
			return "", nil, fmt.Errorf("encode device profile %q index %d: %w", profileName, i, err)
		}
		devices[i] = path
	}
	return profileName, devices, nil
}

// SlurmConfig returns the Gres and GresConf entries for this inventory.
func (g GRESInventory) SlurmConfig() (string, string, error) {
	profileName, devices, err := g.appliedInventoryEntry()
	if err != nil {
		return "", "", err
	}
	if g.GRES.Name == "" {
		return "", "", fmt.Errorf("cannot configure device profile %q with an empty Slurm GRES name", profileName)
	}
	if len(devices) == 0 {
		return "", "", fmt.Errorf("cannot configure device profile %q without devices", profileName)
	}

	gresConf := make([]string, len(devices))
	for i, device := range devices {
		gresConf[i] = fmt.Sprintf("count=1,name=%s,type=%s,file=%s", g.GRES.Name, profileName, device)
	}
	// Dynamic-node GresConf records are separated by '+'. One record per
	// device preserves the array order as Slurm's GRES index order.
	return fmt.Sprintf("%s:%d", g.GRES.String(), len(devices)), strings.Join(gresConf, "+"), nil
}

// AppliedInventory records the DRA device represented by each Slurm index,
// keyed by stable DeviceProfile name.
type AppliedInventory map[string][]DeviceIdentity

// Devices returns the devices represented by indexes for a DeviceProfile.
// The order of the returned devices matches the order of indexes.
func (a AppliedInventory) Devices(profileName string, indexes []int) ([]DeviceIdentity, error) {
	devices, ok := a[profileName]
	if !ok {
		return nil, fmt.Errorf("applied inventory does not contain device profile %q", profileName)
	}

	selected := make([]DeviceIdentity, len(indexes))
	seen := make(map[int]struct{}, len(indexes))
	for i, index := range indexes {
		if index < 0 || index >= len(devices) {
			return nil, fmt.Errorf("Slurm GRES index %d is outside device profile %q inventory of %d devices", index, profileName, len(devices))
		}
		if _, ok := seen[index]; ok {
			return nil, fmt.Errorf("Slurm GRES index %d is repeated for device profile %q", index, profileName)
		}
		seen[index] = struct{}{}
		selected[i] = devices[index]
	}
	return selected, nil
}

type appliedInventoryWire struct {
	Version int `json:"v"`
	// The profile name is also the Slurm GRES type. The GRES name is omitted
	// because the DeviceProfile registry supplies it.
	Profiles map[string][]string `json:"profiles"`
}

// EncodeAppliedInventory encodes indexed GRES inventory for a Slurm node Extra
// field. Device array position is the Slurm GRES index.
func EncodeAppliedInventory(inventory []GRESInventory) (string, error) {
	profiles := make(map[string][]string, len(inventory))
	for _, gres := range inventory {
		profileName, devices, err := gres.appliedInventoryEntry()
		if err != nil {
			return "", err
		}
		if _, ok := profiles[profileName]; ok {
			return "", fmt.Errorf("cannot encode duplicate device profile %q", profileName)
		}
		profiles[profileName] = devices
	}

	data, err := json.Marshal(appliedInventoryWire{
		Version:  appliedInventoryVersion,
		Profiles: profiles,
	})
	if err != nil {
		return "", fmt.Errorf("encode applied inventory: %w", err)
	}
	return AppliedInventoryExtraPrefix + string(data), nil
}

// DecodeAppliedInventory decodes the profile-to-index mapping stored in a
// Slurm node Extra field.
func DecodeAppliedInventory(extra string) (AppliedInventory, error) {
	data, ok := strings.CutPrefix(extra, AppliedInventoryExtraPrefix)
	if !ok {
		return nil, fmt.Errorf("slurm node Extra does not contain a DRA GRES map")
	}

	var wire appliedInventoryWire
	if err := json.Unmarshal([]byte(data), &wire); err != nil {
		return nil, fmt.Errorf("decode applied inventory: %w", err)
	}
	if wire.Version != appliedInventoryVersion {
		return nil, fmt.Errorf("unsupported applied inventory version %d", wire.Version)
	}
	if wire.Profiles == nil {
		return nil, fmt.Errorf("applied inventory has no profiles map")
	}

	inventory := make(AppliedInventory, len(wire.Profiles))
	for profileName, paths := range wire.Profiles {
		if profileName == "" {
			return nil, fmt.Errorf("applied inventory contains an empty device profile name")
		}
		devices := make([]DeviceIdentity, len(paths))
		for i, path := range paths {
			device, err := decodeDevicePath(path)
			if err != nil {
				return nil, fmt.Errorf("decode device profile %q index %d: %w", profileName, i, err)
			}
			devices[i] = device
		}
		inventory[profileName] = devices
	}
	return inventory, nil
}

func encodeDevicePath(device DeviceIdentity) (string, error) {
	driver := device.Driver.String()
	pool := device.Pool.String()
	name := device.Device.String()
	if driver == "" || pool == "" || name == "" {
		return "", fmt.Errorf("DRA device identity must contain a driver, pool, and device name")
	}
	return devicePathPrefix + driver + "/" + pool + "/" + name, nil
}

func decodeDevicePath(path string) (DeviceIdentity, error) {
	identity, ok := strings.CutPrefix(path, devicePathPrefix)
	if !ok {
		return DeviceIdentity{}, fmt.Errorf("device path %q must start with %q", path, devicePathPrefix)
	}
	driver, poolAndDevice, ok := strings.Cut(identity, "/")
	if !ok {
		return DeviceIdentity{}, fmt.Errorf("device path %q must contain a driver, pool, and device name", path)
	}
	lastSlash := strings.LastIndexByte(poolAndDevice, '/')
	if driver == "" || lastSlash <= 0 || lastSlash == len(poolAndDevice)-1 {
		return DeviceIdentity{}, fmt.Errorf("device path %q must contain a driver, pool, and device name", path)
	}
	return structured.MakeDeviceID(driver, poolAndDevice[:lastSlash], poolAndDevice[lastSlash+1:]), nil
}
