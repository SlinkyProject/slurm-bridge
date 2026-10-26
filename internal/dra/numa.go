// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
)

// NUMASpans returns the mean number of NUMA nodes per node covered by each
// named profile's devices. Profiles without NUMA-local devices are absent.
func NUMASpans(ctx context.Context, registry *Registry, resourceSlices []*resourcev1.ResourceSlice, profileNames []string) map[string]float64 {
	if registry == nil || len(profileNames) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(profileNames))
	for _, name := range profileNames {
		wanted[name] = struct{}{}
	}
	nodesByProfile := make(map[string]map[string]map[int]struct{})
	for _, resourceSlice := range resourceSlices {
		nodeName := ptr.Deref(resourceSlice.Spec.NodeName, "")
		profiles := registry.profilesForDriver(resourceSlice.Spec.Driver)
		if nodeName == "" || len(profiles) == 0 {
			continue
		}
		for j := range resourceSlice.Spec.Devices {
			device := &resourceSlice.Spec.Devices[j]
			numaNodes := deviceNUMANodes(device)
			if len(numaNodes) == 0 {
				continue
			}
			identity := structured.MakeDeviceID(resourceSlice.Spec.Driver, resourceSlice.Spec.Pool.Name, device.Name)
			profile, matched, err := matchDeviceProfile(ctx, deviceProfileCELCache, profiles, identity, device)
			if err != nil || !matched {
				continue
			}
			if _, ok := wanted[profile.Name]; !ok {
				continue
			}
			nodes, ok := nodesByProfile[profile.Name]
			if !ok {
				nodes = make(map[string]map[int]struct{})
				nodesByProfile[profile.Name] = nodes
			}
			if nodes[nodeName] == nil {
				nodes[nodeName] = make(map[int]struct{})
			}
			for _, numaNode := range numaNodes {
				nodes[nodeName][numaNode] = struct{}{}
			}
		}
	}
	spans := make(map[string]float64, len(nodesByProfile))
	for name, nodes := range nodesByProfile {
		total := 0
		for _, numaNodes := range nodes {
			total += len(numaNodes)
		}
		spans[name] = float64(total) / float64(len(nodes))
	}
	return spans
}
