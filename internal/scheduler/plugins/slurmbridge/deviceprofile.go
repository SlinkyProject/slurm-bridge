// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/puttsk/hostlist"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

type deviceProfileRequest struct {
	DeviceClassName string
	Profile         dra.DeviceProfile
	Count           int64
}

type indexedGRESAllocation struct {
	deviceProfileRequest
	RequestName string
	Indexes     []int
}

type claimAllocation struct {
	NodeResources          *slurmcontrol.NodeResources
	IndexedGRESAllocations []indexedGRESAllocation
}

func (sb *SlurmBridge) deviceProfileRequests(ctx context.Context, pod *corev1.Pod) ([]deviceProfileRequest, error) {
	counts := deviceClassRequestCounts(pod)
	requests := make([]deviceProfileRequest, 0, len(counts))
	for _, className := range slices.Sorted(maps.Keys(counts)) {
		// TODO: Replace the missing/non-matching DeviceClass fallbacks below with
		// explicit, versioned legacy handling during upgrades. New profile claim
		// creation must fail closed if the live DeviceClass cannot be resolved.
		deviceClass := &resourcev1.DeviceClass{}
		if err := sb.Get(ctx, client.ObjectKey{Name: className}, deviceClass); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("get DeviceClass %q: %w", className, err)
		}
		profile, err := sb.draRegistry.MatchDeviceClass(deviceClass)
		if err != nil {
			// DeviceClasses outside the profile registry continue through the
			// existing driver-specific allocation path.
			continue
		}
		if len(deviceClass.Spec.Config) != 0 {
			return nil, fmt.Errorf("DeviceClass %q configuration is not supported", className)
		}
		requests = append(requests, deviceProfileRequest{
			DeviceClassName: className,
			Profile:         profile,
			Count:           counts[className],
		})
	}
	return requests, nil
}

func allocateIndexedGRESProfiles(requests []deviceProfileRequest, gresResources []slurmcontrol.GresLayout) ([]indexedGRESAllocation, error) {
	profileGRES := make(map[string]dra.GRES, len(requests))
	indexedRequests := make([]deviceProfileRequest, 0, len(requests))
	for _, request := range requests {
		if _, ok := request.Profile.Backend.(dra.IndexedGRESBackend); !ok {
			continue
		}
		gres, err := request.Profile.GRES()
		if err != nil {
			return nil, err
		}
		profileGRES[request.Profile.Name] = gres
		indexedRequests = append(indexedRequests, request)
	}

	indexesByProfile := make(map[string][]int, len(profileGRES))
	for _, resource := range gresResources {
		gres, ok := profileGRES[resource.Type]
		if !ok || resource.Name != gres.Name {
			continue
		}
		indexes, err := expandGRESIndexes(resource)
		if err != nil {
			return nil, err
		}
		indexesByProfile[resource.Type] = append(indexesByProfile[resource.Type], indexes...)
	}

	cursors := make(map[string]int, len(indexesByProfile))
	allocations := make([]indexedGRESAllocation, 0, len(indexedRequests))
	for _, request := range indexedRequests {
		indexes, allocated := indexesByProfile[request.Profile.Name]
		if !allocated {
			// TODO: Replace this implicit fallback with an explicit check for a job
			// created by the legacy flow. A new indexed-GRES profile request without
			// matching allocated profile GRES must fail closed.
			// A legacy-formatted allocation, if present, is handled separately.
			continue
		}
		start := cursors[request.Profile.Name]
		if request.Count > int64(len(indexes)-start) {
			return nil, fmt.Errorf("not enough allocated Slurm GRES indexes for DeviceClass %q: requested %d, allocated %d", request.DeviceClassName, request.Count, len(indexes)-start)
		}
		end := start + int(request.Count)
		allocations = append(allocations, indexedGRESAllocation{
			deviceProfileRequest: request,
			Indexes:              slices.Clone(indexes[start:end]),
		})
		cursors[request.Profile.Name] = end
	}
	return allocations, nil
}

func expandGRESIndexes(gres slurmcontrol.GresLayout) ([]int, error) {
	if strings.TrimSpace(gres.Index) == "" {
		return nil, fmt.Errorf("missing indexes for allocated Slurm GRES %s:%s", gres.Name, gres.Type)
	}
	expanded, err := hostlist.Expand(fmt.Sprintf("[%s]", gres.Index))
	if err != nil {
		return nil, fmt.Errorf("expand indexes for Slurm GRES %s:%s: %w", gres.Name, gres.Type, err)
	}
	indexes := make([]int, len(expanded))
	for i, value := range expanded {
		index, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("parse index %q for Slurm GRES %s:%s: %w", value, gres.Name, gres.Type, err)
		}
		indexes[i] = index
	}
	return indexes, nil
}

// splitGRESResources classifies allocated Slurm GRES by their actual
// representation. A GRES type which is a registered indexed-GRES
// DeviceProfile is handled exclusively by the profile path, even when no
// request currently resolves to it. Profiles using other backends do not claim
// the GRES type namespace.
func splitGRESResources(registry *dra.Registry, resources slurmcontrol.NodeResources) (profileResources, nonProfileResources slurmcontrol.NodeResources, err error) {
	profileResources = resources
	profileResources.Gres = nil
	nonProfileResources = resources
	nonProfileResources.Gres = nil

	for _, resource := range resources.Gres {
		_, owned, err := registry.MatchIndexedGRES(dra.GRES{Name: resource.Name, Type: resource.Type})
		if err != nil {
			return slurmcontrol.NodeResources{}, slurmcontrol.NodeResources{}, err
		}
		if !owned {
			nonProfileResources.Gres = append(nonProfileResources.Gres, resource)
			continue
		}
		profileResources.Gres = append(profileResources.Gres, resource)
	}

	return profileResources, nonProfileResources, nil
}

func appendIndexedGRESRequests(requests []resourcev1.DeviceRequest, allocations []indexedGRESAllocation) ([]resourcev1.DeviceRequest, []indexedGRESAllocation) {
	usedNames := make(map[string]struct{}, len(requests)+len(allocations))
	for _, request := range requests {
		usedNames[request.Name] = struct{}{}
	}
	for i := range allocations {
		gres, _ := allocations[i].Profile.GRES()
		requestName := gres.Name
		for suffix := 2; ; suffix++ {
			if _, used := usedNames[requestName]; !used {
				break
			}
			requestName = fmt.Sprintf("%s-%d", gres.Name, suffix)
		}
		usedNames[requestName] = struct{}{}
		allocations[i].RequestName = requestName
		requests = append(requests, resourcev1.DeviceRequest{
			Name: requestName,
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: allocations[i].DeviceClassName,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           allocations[i].Count,
			},
		})
	}
	return requests, allocations
}

func (sb *SlurmBridge) indexedGRESAllocationResults(ctx context.Context, claim *resourcev1.ResourceClaim, allocation *claimAllocation) ([]resourcev1.DeviceRequestAllocationResult, error) {
	if len(allocation.IndexedGRESAllocations) == 0 {
		return nil, nil
	}
	applied, err := dra.DecodeAppliedInventory(allocation.NodeResources.NodeExtra)
	if err != nil {
		return nil, err
	}

	requests := make(map[string]resourcev1.DeviceRequest, len(claim.Spec.Devices.Requests))
	for _, request := range claim.Spec.Devices.Requests {
		requests[request.Name] = request
	}

	var results []resourcev1.DeviceRequestAllocationResult
	for _, profileAllocation := range allocation.IndexedGRESAllocations {
		request, ok := requests[profileAllocation.RequestName]
		if !ok || request.Exactly == nil || request.Exactly.DeviceClassName != profileAllocation.DeviceClassName {
			return nil, fmt.Errorf("ResourceClaim is missing DeviceProfile request %q for DeviceClass %q", profileAllocation.RequestName, profileAllocation.DeviceClassName)
		}

		deviceClass := &resourcev1.DeviceClass{}
		if err := sb.Get(ctx, client.ObjectKey{Name: profileAllocation.DeviceClassName}, deviceClass); err != nil {
			return nil, fmt.Errorf("get DeviceClass %q while binding: %w", profileAllocation.DeviceClassName, err)
		}
		profile, err := sb.draRegistry.MatchDeviceClass(deviceClass)
		if err != nil {
			return nil, fmt.Errorf("verify DeviceClass %q while binding: %w", profileAllocation.DeviceClassName, err)
		}
		if len(deviceClass.Spec.Config) != 0 {
			return nil, fmt.Errorf("DeviceClass %q configuration is not supported", profileAllocation.DeviceClassName)
		}
		if profile.Name != profileAllocation.Profile.Name {
			return nil, fmt.Errorf("DeviceClass %q changed from DeviceProfile %q to %q before binding", profileAllocation.DeviceClassName, profileAllocation.Profile.Name, profile.Name)
		}

		devices, err := applied.Devices(profile.Name, profileAllocation.Indexes)
		if err != nil {
			return nil, err
		}
		for _, device := range devices {
			if device.Driver.String() != profile.Driver {
				return nil, fmt.Errorf("applied DeviceProfile %q contains driver %q, expected %q", profile.Name, device.Driver.String(), profile.Driver)
			}
			results = append(results, resourcev1.DeviceRequestAllocationResult{
				Request: profileAllocation.RequestName,
				Driver:  device.Driver.String(),
				Pool:    device.Pool.String(),
				Device:  device.Device.String(),
			})
		}
	}
	return results, nil
}
