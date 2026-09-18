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

var deviceProfileCELFeatures = dracel.Features{
	EnableConsumableCapacity: true,
	EnableListTypeAttributes: true,
}

var deviceProfileCELCache = dracel.NewCache(maxDeviceProfiles, deviceProfileCELFeatures)

// DeviceIdentity is the stable DRA identity of a device.
type DeviceIdentity = structured.DeviceID

// OverlappingDeviceProfilesError reports a device which matched more than one
// profile. Devices must resolve to exactly one profile so Slurm cannot account
// for the same physical device through multiple GRES types.
type OverlappingDeviceProfilesError struct {
	Device   DeviceIdentity
	Profiles [2]string
}

func (e *OverlappingDeviceProfilesError) Error() string {
	return fmt.Sprintf("DRA device %q matches overlapping device profiles %q and %q", e.Device.String(), e.Profiles[0], e.Profiles[1])
}

// ProfileInventory contains the devices resolved to one DeviceProfile.
// Devices are ordered within the profile. Their absolute Slurm indexes also
// account for earlier profiles which use the same GRES name.
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

// ResourcePoolID identifies a DRA resource pool within its driver.
type ResourcePoolID struct {
	Driver string
	Pool   string
}

// ResourcePoolIDFromSlice returns the driver and pool identified by a ResourceSlice.
func ResourcePoolIDFromSlice(resourceSlice *resourcev1.ResourceSlice) ResourcePoolID {
	return ResourcePoolID{Driver: resourceSlice.Spec.Driver, Pool: resourceSlice.Spec.Pool.Name}
}

// String returns the driver/pool key used in cache indexes and diagnostics.
func (id ResourcePoolID) String() string {
	return id.Driver + "/" + id.Pool
}

type resourcePoolSnapshot struct {
	ID                 ResourcePoolID
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
	poolSnapshots, err := selectResourcePoolSnapshots(registry, node, resourceSlices)
	if err != nil {
		return NodeInventory{}, err
	}

	profilesByName := make(map[string]DeviceProfile)
	devicesByProfile := make(map[string][]DeviceIdentity)
	seen := make(map[DeviceIdentity]struct{})
	for _, poolSnapshot := range poolSnapshots {
		for _, resourceSlice := range poolSnapshot.Slices {
			for i := range resourceSlice.Spec.Devices {
				device := &resourceSlice.Spec.Devices[i]
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

func selectResourcePoolSnapshots(registry *Registry, node *corev1.Node, resourceSlices []resourcev1.ResourceSlice) ([]resourcePoolSnapshot, error) {
	snapshotsByPool := make(map[ResourcePoolID]*resourcePoolSnapshot)
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
		addResourceSliceToSnapshot(snapshotsByPool, profiles, resourceSlice)
	}
	return completeResourcePoolSnapshots(node, snapshotsByPool)
}

func addResourceSliceToSnapshot(
	snapshotsByPool map[ResourcePoolID]*resourcePoolSnapshot,
	profiles []DeviceProfile,
	resourceSlice *resourcev1.ResourceSlice,
) {
	id := ResourcePoolIDFromSlice(resourceSlice)
	snapshot, ok := snapshotsByPool[id]
	if !ok || resourceSlice.Spec.Pool.Generation > snapshot.Generation {
		snapshotsByPool[id] = &resourcePoolSnapshot{
			ID:                 id,
			Generation:         resourceSlice.Spec.Pool.Generation,
			ResourceSliceCount: resourceSlice.Spec.Pool.ResourceSliceCount,
			Profiles:           profiles,
			Slices:             []*resourcev1.ResourceSlice{resourceSlice},
		}
		return
	}
	if resourceSlice.Spec.Pool.Generation < snapshot.Generation {
		return
	}
	snapshot.Slices = append(snapshot.Slices, resourceSlice)
}

// completeResourcePoolSnapshots validates the highest generation of each pool
// and returns complete snapshots assigned to node. Every slice in a generation
// must name the same single node. Incomplete pools assigned to other nodes are
// ignored so a driver mid-publish does not block inventory across the cluster.
func completeResourcePoolSnapshots(node *corev1.Node, snapshotsByPool map[ResourcePoolID]*resourcePoolSnapshot) ([]resourcePoolSnapshot, error) {
	poolIDs := make([]ResourcePoolID, 0, len(snapshotsByPool))
	for id := range snapshotsByPool {
		poolIDs = append(poolIDs, id)
	}
	slices.SortFunc(poolIDs, func(a, b ResourcePoolID) int {
		if n := cmp.Compare(a.Driver, b.Driver); n != 0 {
			return n
		}
		return cmp.Compare(a.Pool, b.Pool)
	})

	snapshots := make([]resourcePoolSnapshot, 0, len(poolIDs))
	for _, id := range poolIDs {
		snapshot := snapshotsByPool[id]
		nodeName := ptr.Deref(snapshot.Slices[0].Spec.NodeName, "")
		for _, resourceSlice := range snapshot.Slices {
			if err := ValidateResourceSliceNode(resourceSlice); err != nil {
				return nil, err
			}
			if *resourceSlice.Spec.NodeName != nodeName {
				return nil, fmt.Errorf("DRA resource pool %q generation %d has inconsistent nodeName values %q and %q", id.String(), snapshot.Generation, nodeName, *resourceSlice.Spec.NodeName)
			}
			if resourceSlice.Spec.Pool.ResourceSliceCount != snapshot.ResourceSliceCount {
				return nil, fmt.Errorf("DRA resource pool %q generation %d has inconsistent resourceSliceCount values %d and %d", id.String(), snapshot.Generation, snapshot.ResourceSliceCount, resourceSlice.Spec.Pool.ResourceSliceCount)
			}
		}
		if nodeName != node.Name {
			continue
		}
		if int64(len(snapshot.Slices)) != snapshot.ResourceSliceCount {
			return nil, fmt.Errorf("DRA resource pool %q generation %d is incomplete: found %d of %d ResourceSlices", id.String(), snapshot.Generation, len(snapshot.Slices), snapshot.ResourceSliceCount)
		}
		snapshots = append(snapshots, *snapshot)
	}
	return snapshots, nil
}

// ValidateResourceSliceNode requires every device in the slice to belong to
// the single node explicitly named by spec.nodeName.
func ValidateResourceSliceNode(resourceSlice *resourcev1.ResourceSlice) error {
	if resourceSlice.Spec.NodeSelector != nil {
		return fmt.Errorf("ResourceSlice %q uses unsupported spec.nodeSelector", resourceSlice.Name)
	}
	if ptr.Deref(resourceSlice.Spec.AllNodes, false) {
		return fmt.Errorf("ResourceSlice %q uses unsupported spec.allNodes", resourceSlice.Name)
	}
	if ptr.Deref(resourceSlice.Spec.PerDeviceNodeSelection, false) {
		return fmt.Errorf("ResourceSlice %q uses unsupported spec.perDeviceNodeSelection", resourceSlice.Name)
	}
	if ptr.Deref(resourceSlice.Spec.NodeName, "") == "" {
		return fmt.Errorf("ResourceSlice %q must have a nonempty spec.nodeName", resourceSlice.Name)
	}
	return nil
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
			return DeviceProfile{}, false, &OverlappingDeviceProfilesError{
				Device:   identity,
				Profiles: [2]string{matchedProfile.Name, profile.Name},
			}
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
