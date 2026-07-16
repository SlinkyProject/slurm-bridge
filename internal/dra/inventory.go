// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
)

var deviceProfileCELCache = dracel.NewCache(10, dracel.Features{
	EnableConsumableCapacity: true,
	EnableListTypeAttributes: true,
})

// DeviceIdentity is the stable DRA identity of a device.
type DeviceIdentity = structured.DeviceID

// ProfileInventory contains the devices resolved to one DeviceProfile.
// Devices are ordered by Slurm index: Devices[i] is allocated as index i.
type ProfileInventory struct {
	Profile DeviceProfile
	Devices []DeviceIdentity
}

// NodeInventory is the profile-classified device inventory computed for one
// Kubernetes node. It does not describe current allocation availability.
// Profiles are ordered by DeviceProfile name.
type NodeInventory struct {
	NodeName string
	Profiles []ProfileInventory
}

// HasDriver reports whether the node inventory contains a profile for driver.
func (n NodeInventory) HasDriver(driver string) bool {
	for _, inventory := range n.Profiles {
		if inventory.Profile.Driver == driver {
			return true
		}
	}
	return false
}

type resourcePoolID struct {
	Driver string
	Pool   string
}

type resourcePoolSnapshot struct {
	ID                 resourcePoolID
	Generation         int64
	ResourceSliceCount int64
	Profiles           []DeviceProfile
	Slices             []*resourcev1.ResourceSlice
}

// BuildNodeInventory builds the inventory for node from ResourceSlices
// matching profiles in registry. Matching a profile classifies a device; it
// does not determine whether that device is currently allocatable.
func BuildNodeInventory(ctx context.Context, registry *Registry, node *corev1.Node, resourceSlices []resourcev1.ResourceSlice) (NodeInventory, error) {
	if registry == nil {
		return NodeInventory{}, fmt.Errorf("device profile registry must not be nil")
	}
	if node == nil {
		return NodeInventory{}, fmt.Errorf("node must not be nil")
	}
	poolSnapshots, err := selectResourcePoolSnapshots(registry, resourceSlices)
	if err != nil {
		return NodeInventory{}, err
	}

	profilesByName := make(map[string]DeviceProfile)
	devicesByProfile := make(map[string][]DeviceIdentity)
	seen := make(map[DeviceIdentity]struct{})
	for _, poolSnapshot := range poolSnapshots {
		for _, resourceSlice := range poolSnapshot.Slices {
			devices, err := devicesAccessibleToNode(node, resourceSlice)
			if err != nil {
				return NodeInventory{}, err
			}
			for _, device := range devices {
				identity := structured.MakeDeviceID(resourceSlice.Spec.Driver, resourceSlice.Spec.Pool.Name, device.Name)
				profile, matched, err := matchDeviceProfile(ctx, deviceProfileCELCache, poolSnapshot.Profiles, identity, device)
				if err != nil {
					return NodeInventory{}, err
				}
				if !matched {
					continue
				}
				if _, ok := seen[identity]; ok {
					return NodeInventory{}, fmt.Errorf("duplicate DRA device identity %q", identity.String())
				}
				seen[identity] = struct{}{}
				profilesByName[profile.Name] = profile
				devicesByProfile[profile.Name] = append(devicesByProfile[profile.Name], identity)
			}
		}
	}

	return NodeInventory{
		NodeName: node.Name,
		Profiles: sortedProfileInventories(profilesByName, devicesByProfile),
	}, nil
}

func selectResourcePoolSnapshots(registry *Registry, resourceSlices []resourcev1.ResourceSlice) ([]resourcePoolSnapshot, error) {
	snapshotsByPool := make(map[resourcePoolID]*resourcePoolSnapshot)
	profilesByDriver := make(map[string][]DeviceProfile)
	for i := range resourceSlices {
		resourceSlice := &resourceSlices[i]
		profiles, ok := profilesByDriver[resourceSlice.Spec.Driver]
		if !ok {
			profiles = registry.profilesForDriver(resourceSlice.Spec.Driver)
			profilesByDriver[resourceSlice.Spec.Driver] = profiles
		}
		if len(profiles) == 0 {
			continue
		}
		if err := addResourceSliceToSnapshot(snapshotsByPool, profiles, resourceSlice); err != nil {
			return nil, err
		}
	}
	return completeResourcePoolSnapshots(snapshotsByPool)
}

func addResourceSliceToSnapshot(
	snapshotsByPool map[resourcePoolID]*resourcePoolSnapshot,
	profiles []DeviceProfile,
	resourceSlice *resourcev1.ResourceSlice,
) error {
	id := resourcePoolID{Driver: resourceSlice.Spec.Driver, Pool: resourceSlice.Spec.Pool.Name}
	snapshot, ok := snapshotsByPool[id]
	if !ok || resourceSlice.Spec.Pool.Generation > snapshot.Generation {
		snapshotsByPool[id] = &resourcePoolSnapshot{
			ID:                 id,
			Generation:         resourceSlice.Spec.Pool.Generation,
			ResourceSliceCount: resourceSlice.Spec.Pool.ResourceSliceCount,
			Profiles:           profiles,
			Slices:             []*resourcev1.ResourceSlice{resourceSlice},
		}
		return nil
	}
	if resourceSlice.Spec.Pool.Generation < snapshot.Generation {
		return nil
	}
	if resourceSlice.Spec.Pool.ResourceSliceCount != snapshot.ResourceSliceCount {
		return fmt.Errorf("DRA resource pool %q generation %d has inconsistent resourceSliceCount values %d and %d", id.Driver+"/"+id.Pool, snapshot.Generation, snapshot.ResourceSliceCount, resourceSlice.Spec.Pool.ResourceSliceCount)
	}
	snapshot.Slices = append(snapshot.Slices, resourceSlice)
	return nil
}

func completeResourcePoolSnapshots(snapshotsByPool map[resourcePoolID]*resourcePoolSnapshot) ([]resourcePoolSnapshot, error) {
	poolIDs := make([]resourcePoolID, 0, len(snapshotsByPool))
	for id := range snapshotsByPool {
		poolIDs = append(poolIDs, id)
	}
	slices.SortFunc(poolIDs, func(a, b resourcePoolID) int {
		if n := cmp.Compare(a.Driver, b.Driver); n != 0 {
			return n
		}
		return cmp.Compare(a.Pool, b.Pool)
	})

	snapshots := make([]resourcePoolSnapshot, 0, len(poolIDs))
	for _, id := range poolIDs {
		snapshot := snapshotsByPool[id]
		if int64(len(snapshot.Slices)) != snapshot.ResourceSliceCount {
			return nil, fmt.Errorf("DRA resource pool %q generation %d is incomplete: found %d of %d ResourceSlices", id.Driver+"/"+id.Pool, snapshot.Generation, len(snapshot.Slices), snapshot.ResourceSliceCount)
		}
		snapshots = append(snapshots, *snapshot)
	}
	return snapshots, nil
}

func devicesAccessibleToNode(node *corev1.Node, resourceSlice *resourcev1.ResourceSlice) ([]*resourcev1.Device, error) {
	if ptr.Deref(resourceSlice.Spec.PerDeviceNodeSelection, false) {
		devices := make([]*resourcev1.Device, 0, len(resourceSlice.Spec.Devices))
		for i := range resourceSlice.Spec.Devices {
			device := &resourceSlice.Spec.Devices[i]
			matches, err := structured.NodeMatches(
				structured.Features{PartitionableDevices: true},
				node,
				ptr.Deref(device.NodeName, ""),
				ptr.Deref(device.AllNodes, false),
				device.NodeSelector,
			)
			if err != nil {
				return nil, fmt.Errorf("match device %q from ResourceSlice %q to node %q: %w", device.Name, resourceSlice.Name, node.Name, err)
			}
			if matches {
				devices = append(devices, device)
			}
		}
		return devices, nil
	}

	matches, err := structured.NodeMatches(
		structured.Features{},
		node,
		ptr.Deref(resourceSlice.Spec.NodeName, ""),
		ptr.Deref(resourceSlice.Spec.AllNodes, false),
		resourceSlice.Spec.NodeSelector,
	)
	if err != nil {
		return nil, fmt.Errorf("match ResourceSlice %q to node %q: %w", resourceSlice.Name, node.Name, err)
	}
	if !matches {
		return nil, nil
	}
	devices := make([]*resourcev1.Device, len(resourceSlice.Spec.Devices))
	for i := range resourceSlice.Spec.Devices {
		devices[i] = &resourceSlice.Spec.Devices[i]
	}
	return devices, nil
}

// ResourceSliceMatchesNode reports whether a ResourceSlice contains at least
// one device accessible to node.
func ResourceSliceMatchesNode(node *corev1.Node, resourceSlice *resourcev1.ResourceSlice) (bool, error) {
	devices, err := devicesAccessibleToNode(node, resourceSlice)
	return len(devices) > 0, err
}

func matchDeviceProfile(
	ctx context.Context,
	celCache *dracel.Cache,
	profiles []DeviceProfile,
	identity DeviceIdentity,
	device *resourcev1.Device,
) (DeviceProfile, bool, error) {
	var matchedProfile DeviceProfile
	matched := false
	for _, profile := range profiles {
		compiled := celCache.GetOrCompile(profile.Selector)
		if compiled.Error != nil {
			return DeviceProfile{}, false, fmt.Errorf("compile selector for device profile %q: %w", profile.Name, compiled.Error)
		}
		matches, _, err := compiled.DeviceMatches(ctx, dracel.Device{
			Driver:                   identity.Driver.String(),
			AllowMultipleAllocations: device.AllowMultipleAllocations,
			Attributes:               device.Attributes,
			Capacity:                 device.Capacity,
		})
		if err != nil {
			return DeviceProfile{}, false, fmt.Errorf("evaluate device profile %q for device %q: %w", profile.Name, identity.String(), err)
		}
		if !matches {
			continue
		}
		if matched {
			return DeviceProfile{}, false, fmt.Errorf("DRA device %q matches overlapping device profiles %q and %q", identity.String(), matchedProfile.Name, profile.Name)
		}
		matchedProfile = profile
		matched = true
	}
	return matchedProfile, matched, nil
}

func sortedProfileInventories(profilesByName map[string]DeviceProfile, devicesByProfile map[string][]DeviceIdentity) []ProfileInventory {
	profileNames := make([]string, 0, len(devicesByProfile))
	for name := range devicesByProfile {
		profileNames = append(profileNames, name)
	}
	slices.Sort(profileNames)

	var inventories []ProfileInventory
	for _, name := range profileNames {
		devices := devicesByProfile[name]
		slices.SortFunc(devices, func(a, b DeviceIdentity) int {
			if n := cmp.Compare(a.Driver.String(), b.Driver.String()); n != 0 {
				return n
			}
			if n := cmp.Compare(a.Pool.String(), b.Pool.String()); n != 0 {
				return n
			}
			return cmp.Compare(a.Device.String(), b.Device.String())
		})
		inventories = append(inventories, ProfileInventory{
			Profile: profilesByName[name],
			Devices: devices,
		})
	}
	return inventories
}
