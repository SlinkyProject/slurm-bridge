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

type coreBitmapAllocation struct {
	deviceProfileRequest
	RequestName string
}

type claimAllocation struct {
	NodeResources          *slurmcontrol.NodeResources
	CoreBitmapAllocation   *coreBitmapAllocation
	IndexedGRESAllocations []indexedGRESAllocation
}

func (sb *SlurmBridge) deviceProfileRequests(ctx context.Context, pod *corev1.Pod) ([]deviceProfileRequest, error) {
	counts := deviceClassRequestCounts(pod)
	requests := make([]deviceProfileRequest, 0, len(counts))
	for _, className := range slices.Sorted(maps.Keys(counts)) {
		deviceClass := &resourcev1.DeviceClass{}
		if err := sb.Get(ctx, client.ObjectKey{Name: className}, deviceClass); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("DeviceClass %q was not found", className)
			}
			return nil, fmt.Errorf("get DeviceClass %q: %w", className, err)
		}
		profile, err := sb.draRegistry.MatchDeviceClass(deviceClass)
		if err != nil {
			return nil, err
		}
		requests = append(requests, deviceProfileRequest{
			DeviceClassName: className,
			Profile:         profile,
			Count:           counts[className],
		})
	}
	return requests, nil
}

func profileRequestsWithoutLegacyAllocations(requests []deviceProfileRequest, resources []slurmcontrol.GresLayout) []deviceProfileRequest {
	legacyClasses := make(map[string]struct{})
	for _, resource := range resources {
		if isLegacyGPUDriver(resource.Type) {
			legacyClasses[resource.Type] = struct{}{}
		}
	}
	filtered := make([]deviceProfileRequest, 0, len(requests))
	for _, request := range requests {
		if _, legacy := legacyClasses[request.DeviceClassName]; !legacy {
			filtered = append(filtered, request)
		}
	}
	return filtered
}

func allocateCoreBitmapProfile(requests []deviceProfileRequest) (*coreBitmapAllocation, error) {
	var allocation *coreBitmapAllocation
	for _, request := range requests {
		if !request.Profile.UsesCoreBitmap() {
			continue
		}
		if allocation != nil {
			return nil, fmt.Errorf("multiple DeviceClasses %q and %q resolve to the core-bitmap backend", allocation.DeviceClassName, request.DeviceClassName)
		}
		allocation = &coreBitmapAllocation{
			deviceProfileRequest: request,
			RequestName:          corev1.ResourceCPU.String(),
		}
	}
	return allocation, nil
}

func allocateIndexedGRESProfiles(requests []deviceProfileRequest, gresResources []slurmcontrol.GresLayout) ([]indexedGRESAllocation, error) {
	profileGRES := make(map[string]dra.GRES, len(requests))
	indexedRequests := make([]deviceProfileRequest, 0, len(requests))
	for _, request := range requests {
		if !request.Profile.UsesIndexedGRES() {
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
			return nil, fmt.Errorf("DeviceClass %q resolves to DeviceProfile %q but the Slurm allocation has no matching indexed GRES", request.DeviceClassName, request.Profile.Name)
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
// request currently resolves to it. Core-bitmap profiles have no GRES
// representation, so their profile names do not claim the GRES type namespace.
// All other GRES remain available to their existing allocation paths,
// including classic device plugins and unrelated custom GRES. Legacy DRA
// compatibility recognizes only the explicitly supported driver-named types
// in legacy.go.
func splitGRESResources(registry *dra.Registry, resources slurmcontrol.NodeResources) (indexedGRESResources, remainingResources slurmcontrol.NodeResources, err error) {
	indexedGRESResources = resources
	indexedGRESResources.Gres = nil
	remainingResources = resources
	remainingResources.Gres = nil

	for _, resource := range resources.Gres {
		_, owned, err := registry.MatchIndexedGRES(dra.GRES{Name: resource.Name, Type: resource.Type})
		if err != nil {
			return slurmcontrol.NodeResources{}, slurmcontrol.NodeResources{}, err
		}
		if !owned {
			remainingResources.Gres = append(remainingResources.Gres, resource)
			continue
		}
		indexedGRESResources.Gres = append(indexedGRESResources.Gres, resource)
	}

	return indexedGRESResources, remainingResources, nil
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

func (sb *SlurmBridge) verifyDeviceProfileRequest(ctx context.Context, claim *resourcev1.ResourceClaim, allocation deviceProfileRequest, requestName string) (dra.DeviceProfile, error) {
	var claimRequest *resourcev1.DeviceRequest
	for i := range claim.Spec.Devices.Requests {
		request := &claim.Spec.Devices.Requests[i]
		if request.Name == requestName {
			claimRequest = request
			break
		}
	}
	if claimRequest == nil || claimRequest.Exactly == nil || claimRequest.Exactly.DeviceClassName != allocation.DeviceClassName {
		return dra.DeviceProfile{}, fmt.Errorf("ResourceClaim is missing DeviceProfile request %q for DeviceClass %q", requestName, allocation.DeviceClassName)
	}

	deviceClass := &resourcev1.DeviceClass{}
	if err := sb.Get(ctx, client.ObjectKey{Name: allocation.DeviceClassName}, deviceClass); err != nil {
		return dra.DeviceProfile{}, fmt.Errorf("get DeviceClass %q while binding: %w", allocation.DeviceClassName, err)
	}
	profile, err := sb.draRegistry.MatchDeviceClass(deviceClass)
	if err != nil {
		return dra.DeviceProfile{}, fmt.Errorf("verify DeviceClass %q while binding: %w", allocation.DeviceClassName, err)
	}
	if profile.Name != allocation.Profile.Name {
		return dra.DeviceProfile{}, fmt.Errorf("DeviceClass %q changed from DeviceProfile %q to %q before binding", allocation.DeviceClassName, allocation.Profile.Name, profile.Name)
	}
	return profile, nil
}

func (sb *SlurmBridge) verifyCoreBitmapRequest(ctx context.Context, claim *resourcev1.ResourceClaim, allocation *coreBitmapAllocation) error {
	profile, err := sb.verifyDeviceProfileRequest(ctx, claim, allocation.deviceProfileRequest, allocation.RequestName)
	if err != nil {
		return err
	}
	if !profile.UsesCoreBitmap() {
		return fmt.Errorf("DeviceClass %q no longer resolves to the core-bitmap backend", allocation.DeviceClassName)
	}
	return nil
}

func (sb *SlurmBridge) indexedGRESAllocationResults(ctx context.Context, claim *resourcev1.ResourceClaim, allocation *claimAllocation) ([]resourcev1.DeviceRequestAllocationResult, error) {
	if len(allocation.IndexedGRESAllocations) == 0 {
		return nil, nil
	}
	applied, err := dra.DecodeAppliedInventory(allocation.NodeResources.NodeExtra)
	if err != nil {
		return nil, err
	}

	var results []resourcev1.DeviceRequestAllocationResult
	for _, profileAllocation := range allocation.IndexedGRESAllocations {
		profile, err := sb.verifyDeviceProfileRequest(ctx, claim, profileAllocation.deviceProfileRequest, profileAllocation.RequestName)
		if err != nil {
			return nil, err
		}
		if !profile.UsesIndexedGRES() {
			return nil, fmt.Errorf("DeviceClass %q no longer resolves to the indexed-GRES backend", profileAllocation.DeviceClassName)
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
