// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

// TODO: Remove this file once upgrades from driver-named GPU GRES are no
// longer supported. New allocations use DeviceProfiles and indexed GRES.
const (
	legacyDRAExampleDriver            = "gpu.example.com"
	legacyDRANVIDIADriver             = "gpu.nvidia.com"
	legacyDRAExampleGPUIndexAttribute = resourcev1.QualifiedName("index")
)

type legacyGPUDevice struct {
	pool string
	name string
}

type legacyGPUInventory map[string]map[int]legacyGPUDevice

func legacyGPUDeviceRequests(ctx context.Context, kubeClient client.Client, resources []slurmcontrol.GresLayout) ([]resourcev1.DeviceRequest, error) {
	requests := make([]resourcev1.DeviceRequest, 0, len(resources))
	for _, gres := range resources {
		if !isLegacyGPUDriver(gres.Type) {
			continue
		}
		exists, err := legacyDeviceClassExists(ctx, kubeClient, gres.Type)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		indexes, err := expandGRESIndexes(gres)
		if err != nil {
			return nil, err
		}

		var selector string
		switch gres.Type {
		case legacyDRAExampleDriver:
			values := make([]string, len(indexes))
			for i, index := range indexes {
				values[i] = strconv.Itoa(index)
			}
			selector = fmt.Sprintf("device.attributes['%s'].index in [%s]", gres.Type, strings.Join(values, ","))
		case legacyDRANVIDIADriver:
			values := make([]string, len(indexes))
			for i, index := range indexes {
				values[i] = fmt.Sprintf("'gpu-%d'", index)
			}
			selector = fmt.Sprintf("device.attributes['%s'].name in [%s]", gres.Type, strings.Join(values, ","))
		}

		requests = append(requests, resourcev1.DeviceRequest{
			Name: gres.Name,
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: gres.Type,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           gres.Count,
				Selectors: []resourcev1.DeviceSelector{{
					CEL: &resourcev1.CELDeviceSelector{Expression: selector},
				}},
			},
		})
	}
	return requests, nil
}

func legacyGPUAllocationResults(ctx context.Context, kubeClient client.Client, nodeName string, resources []slurmcontrol.GresLayout) ([]resourcev1.DeviceRequestAllocationResult, error) {
	if !hasLegacyGPUResources(resources) {
		return nil, nil
	}
	inventory, err := loadLegacyGPUInventory(ctx, kubeClient, nodeName)
	if err != nil {
		return nil, err
	}

	var results []resourcev1.DeviceRequestAllocationResult
	for _, gres := range resources {
		if !isLegacyGPUDriver(gres.Type) {
			continue
		}
		exists, err := legacyDeviceClassExists(ctx, kubeClient, gres.Type)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		indexes, err := expandGRESIndexes(gres)
		if err != nil {
			return nil, err
		}
		for _, index := range indexes {
			device, ok := inventory[gres.Type][index]
			if !ok {
				return nil, fmt.Errorf("gpu index %d from legacy Slurm allocation not found on node", index)
			}
			results = append(results, resourcev1.DeviceRequestAllocationResult{
				Request: gres.Name,
				Driver:  gres.Type,
				Pool:    device.pool,
				Device:  device.name,
			})
		}
	}
	return results, nil
}

func loadLegacyGPUInventory(ctx context.Context, kubeClient client.Client, nodeName string) (legacyGPUInventory, error) {
	resourceSlices := &resourcev1.ResourceSliceList{}
	if err := kubeClient.List(ctx, resourceSlices); err != nil {
		return nil, err
	}

	inventory := make(legacyGPUInventory)
	for i := range resourceSlices.Items {
		resourceSlice := &resourceSlices.Items[i]
		if ptr.Deref(resourceSlice.Spec.NodeName, "") != nodeName || !isLegacyGPUDriver(resourceSlice.Spec.Driver) {
			continue
		}
		devices := inventory[resourceSlice.Spec.Driver]
		if devices == nil {
			devices = make(map[int]legacyGPUDevice)
			inventory[resourceSlice.Spec.Driver] = devices
		}
		for _, device := range resourceSlice.Spec.Devices {
			index := -1
			switch resourceSlice.Spec.Driver {
			case legacyDRAExampleDriver:
				index = int(ptr.Deref(device.Attributes[legacyDRAExampleGPUIndexAttribute].IntValue, -1))
			case legacyDRANVIDIADriver:
				index = legacyNVIDIAGPUNameToIndex(device.Name)
			}
			if index >= 0 {
				devices[index] = legacyGPUDevice{pool: resourceSlice.Spec.Pool.Name, name: device.Name}
			}
		}
	}
	return inventory, nil
}

func legacyNVIDIAGPUNameToIndex(name string) int {
	value, found := strings.CutPrefix(name, "gpu-")
	if !found {
		return -1
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 {
		return -1
	}
	return index
}

func legacyDeviceClassExists(ctx context.Context, kubeClient client.Client, name string) (bool, error) {
	deviceClass := &resourcev1.DeviceClass{}
	err := kubeClient.Get(ctx, types.NamespacedName{Name: name}, deviceClass)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get legacy DeviceClass %q: %w", name, err)
	}
	return true, nil
}

func isLegacyGPUDriver(driver string) bool {
	return driver == legacyDRAExampleDriver || driver == legacyDRANVIDIADriver
}

func hasLegacyGPUResources(resources []slurmcontrol.GresLayout) bool {
	for _, resource := range resources {
		if isLegacyGPUDriver(resource.Type) {
			return true
		}
	}
	return false
}
