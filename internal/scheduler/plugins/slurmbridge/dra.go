// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2024 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-client/pkg/hostlist"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// manageResourceClaim creates DRA ResourceClaims for Slurm GRES allocations
// resolved through an indexed-GRES DeviceProfile or the legacy driver-specific
// path. Core-bitmap profiles are allocated from Slurm's CPU bitmap.
func (sb *SlurmBridge) manageResourceClaim(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) error {
	if resources == nil {
		return nil
	}
	if pod.UID == "" {
		return fmt.Errorf("pod UID is required for allocation recovery")
	}
	claim, mappings, allocation, prepareErr := sb.createRequestsAndMappings(ctx, pod, nodeName, resources)
	if prepareErr == nil && claim != nil {
		// Deterministic names allow create-first; retries recover through a live GET.
		if err := sb.Create(ctx, claim); err == nil {
			return sb.allocateAndPublishClaim(ctx, claim, pod, nodeName, allocation, mappings)
		} else if !apierrors.IsAlreadyExists(err) {
			// Ambiguous writes are recovered on the next attempt.
			return err
		}
	}

	// Recover existing allocations even if inventory or classes have changed.
	existing := &resourcev1.ResourceClaim{}
	if err := sb.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: allocationClaimName(pod)}, existing); err != nil {
		if apierrors.IsNotFound(err) && (prepareErr != nil || claim == nil) {
			return prepareErr
		}
		return err
	}
	if err := validateClaimIdentity(existing, pod, nodeName, resources); err != nil {
		return err
	}
	if existing.Status.Allocation != nil {
		mappings, err := requestMappingsFromClaim(pod, existing)
		if err != nil {
			return err
		}
		return sb.patchPodExtendedResourceClaimStatus(ctx, pod, existing, mappings)
	}
	if prepareErr != nil || claim == nil {
		return prepareErr
	}
	if !equality.Semantic.DeepEqual(existing.Spec, claim.Spec) {
		return fmt.Errorf("existing claim %q differs from Slurm allocation", existing.Name)
	}
	return sb.allocateAndPublishClaim(ctx, existing, pod, nodeName, allocation, mappings)
}

func (sb *SlurmBridge) allocateAndPublishClaim(ctx context.Context, claim *resourcev1.ResourceClaim, pod *corev1.Pod, nodeName string, allocation *claimAllocation, mappings []corev1.ContainerExtendedResourceRequest) error {
	// Retain partial claims for retries. The Pod controller deletes them after
	// completion or cancellation.
	if err := sb.bindClaim(ctx, claim, pod, nodeName, allocation); err != nil {
		return err
	}
	return sb.patchPodExtendedResourceClaimStatus(ctx, pod, claim, mappings)
}

func (sb *SlurmBridge) allocateCPUs(ctx context.Context, nodeName string, resources *slurmcontrol.NodeResources, className, allocationKey string) (*nodeinfo.CPUAllocation, error) {
	if sb.cpuTopology != nil {
		return sb.cpuTopology.AllocateCPUs(ctx, sb.Client, nodeName, resources, className, allocationKey)
	}
	slices, err := nodeutils.GetResourceSlicesForNode(ctx, sb.Client, nodeName)
	if err != nil {
		return nil, err
	}
	current, err := nodeinfo.NewNodeInfoFromResourceSlices(nodeName, slices)
	if err != nil {
		return nil, err
	}
	if err := current.ValidateCPUFingerprint(resources.NodeExtra); err != nil {
		return nil, err
	}
	return current.AllocateCPUs(ctx, sb.Client, resources, className, allocationKey)
}

func (sb *SlurmBridge) createRequestsAndMappings(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) (*resourcev1.ResourceClaim, []corev1.ContainerExtendedResourceRequest, *claimAllocation, error) {
	if pod == nil {
		return nil, nil, nil, errors.New("expected a pod to be given")
	}
	if resources == nil {
		return nil, nil, nil, errors.New("expected node resources")
	}

	profileRequests, err := sb.registry().ResolvePod(ctx, sb.Client, pod)
	if err != nil {
		return nil, nil, nil, err
	}
	indexedGRESResources, remainingResources, err := splitGRESResources(sb.registry(), *resources)
	if err != nil {
		return nil, nil, nil, err
	}

	allocationRequests := profileRequestsWithoutLegacyAllocations(profileRequests, remainingResources.Gres)
	coreBitmapAllocation, err := allocateCoreBitmapProfile(allocationRequests)
	if err != nil {
		return nil, nil, nil, err
	}
	indexedGRESAllocations, err := allocateIndexedGRESProfiles(allocationRequests, indexedGRESResources.Gres)
	if err != nil {
		return nil, nil, nil, err
	}
	var allocatedRequests []resourcev1.DeviceRequest
	if coreBitmapAllocation != nil {
		cpu, err := sb.allocateCPUs(ctx, nodeName, &remainingResources, coreBitmapAllocation.DeviceClassName, allocationClaimName(pod))
		if err != nil {
			return nil, nil, nil, err
		}
		if cpu.CPUCount < coreBitmapAllocation.Count {
			return nil, nil, nil, fmt.Errorf("not enough CPUs in Slurm allocation: requested %d, allocated %d", coreBitmapAllocation.Count, cpu.CPUCount)
		}
		coreBitmapAllocation.CPU = cpu
		coreBitmapAllocation.AllocatedCount = cpu.CPUCount
		allocatedRequests = append(allocatedRequests, cpu.Requests...)
	}
	legacyRequests, err := legacyGPUDeviceRequests(ctx, sb.Client, remainingResources.Gres)
	if err != nil {
		return nil, nil, nil, err
	}
	allocatedRequests = append(allocatedRequests, legacyRequests...)

	requestedCounts := make(map[string]int64, len(profileRequests))
	for _, request := range profileRequests {
		requestedCounts[request.DeviceClassName] = request.Count
	}
	for _, allocation := range indexedGRESAllocations {
		delete(requestedCounts, allocation.DeviceClassName)
	}
	if coreBitmapAllocation != nil {
		delete(requestedCounts, coreBitmapAllocation.DeviceClassName)
	}
	claimResources, err := subsetGRESResources(remainingResources, requestedCounts, deviceClassNames(allocatedRequests))
	if err != nil {
		return nil, nil, nil, err
	}

	var deviceRequests []resourcev1.DeviceRequest
	if coreBitmapAllocation != nil {
		deviceRequests = append(deviceRequests, coreBitmapAllocation.CPU.Requests...)
	}
	legacyRequests, err = legacyGPUDeviceRequests(ctx, sb.Client, claimResources.Gres)
	if err != nil {
		return nil, nil, nil, err
	}
	deviceRequests = append(deviceRequests, legacyRequests...)
	deviceRequests, indexedGRESAllocations = appendIndexedGRESRequests(deviceRequests, indexedGRESAllocations)

	mappings := createContainerRequestMappings(profileRequests, deviceRequests)

	if len(deviceRequests) == 0 || len(mappings) == 0 {
		return nil, nil, nil, nil
	}

	claim := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  pod.Namespace,
			Name:       allocationClaimName(pod),
			Finalizers: []string{resourcev1.Finalizer},
			Labels:     map[string]string{wellknown.LabelAllocationPodUID: string(pod.UID)},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "v1",
					Kind:               "Pod",
					Name:               pod.Name,
					UID:                pod.UID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
			Annotations: map[string]string{
				resourcev1.ExtendedResourceClaimAnnotation: "true",
				allocationIdentityAnnotation:               allocationIdentity(pod, nodeName, resources),
			},
		},
		Spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: deviceRequests,
			},
		},
	}

	if len(mappings) > 0 {
		raw, err := json.Marshal(mappings)
		if err != nil {
			return nil, nil, nil, err
		}
		claim.Annotations[requestMappingsAnnotation] = string(raw)
	}
	if coreBitmapAllocation != nil {
		claim.Spec.Devices.Config = coreBitmapAllocation.CPU.Config
	}

	return claim, mappings, &claimAllocation{
		NodeResources:          claimResources,
		CoreBitmapAllocation:   coreBitmapAllocation,
		IndexedGRESAllocations: indexedGRESAllocations,
	}, nil
}

// bindClaim gets called for claims which are not reserved for the pod yet.
// It might not even be allocated. bindClaim then ensures that the allocation
// and reservation are recorded.
func (sb *SlurmBridge) bindClaim(
	ctx context.Context,
	claim *resourcev1.ResourceClaim,
	pod *corev1.Pod,
	nodeName string,
	resources *claimAllocation,
) error {
	if resources == nil || resources.NodeResources == nil {
		return errors.New("expected claim allocation resources")
	}
	deviceClasses, err := sb.deviceClassesForClaim(ctx, claim)
	if err != nil {
		return err
	}
	configs, err := deviceClassAllocationConfigs(claim, deviceClasses)
	if err != nil {
		return err
	}
	var devices []resourcev1.DeviceRequestAllocationResult
	if resources.CoreBitmapAllocation != nil {
		if err := sb.verifyCoreBitmapRequest(claim, resources.CoreBitmapAllocation, deviceClasses); err != nil {
			return err
		}
		devices = append(devices, resources.CoreBitmapAllocation.CPU.Results...)
	}
	legacyDevices, err := legacyGPUAllocationResults(ctx, sb.Client, nodeName, resources.NodeResources.Gres)
	if err != nil {
		return err
	}
	devices = append(devices, legacyDevices...)
	indexedGRESDevices, err := sb.indexedGRESAllocationResults(claim, resources, deviceClasses)
	if err != nil {
		return err
	}
	devices = append(devices, indexedGRESDevices...)
	if err := validateDeviceProfileAllocationCounts(resources, devices); err != nil {
		return err
	}

	toUpdate := claim.DeepCopy()

	toUpdate.Status.Allocation = &resourcev1.AllocationResult{
		AllocationTimestamp: &metav1.Time{
			Time: time.Now(),
		},
		Devices: resourcev1.DeviceAllocationResult{
			Results: devices,
			Config:  configs,
		},
		NodeSelector: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchFields: []corev1.NodeSelectorRequirement{
						{
							Key:      "metadata.name",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{nodeName},
						},
					},
				},
			},
		},
	}

	for _, config := range claim.Spec.Devices.Config {
		toUpdate.Status.Allocation.Devices.Config = append(toUpdate.Status.Allocation.Devices.Config, resourcev1.DeviceAllocationConfiguration{Source: resourcev1.AllocationConfigSourceClaim, Requests: config.Requests, DeviceConfiguration: config.DeviceConfiguration})
	}
	toUpdate.Status.ReservedFor = []resourcev1.ResourceClaimConsumerReference{
		{Resource: "pods", Name: pod.Name, UID: pod.UID},
	}

	if err := sb.patchClaimAllocation(ctx, claim, toUpdate); err != nil {
		return fmt.Errorf("failed to add reservation to claim %s status: %w", klog.KObj(claim), err)
	}

	// Patch returns the persisted claim; no readback is needed.
	*claim = *toUpdate

	return nil
}

func (sb *SlurmBridge) deviceClassesForClaim(ctx context.Context, claim *resourcev1.ResourceClaim) (map[string]*resourcev1.DeviceClass, error) {
	deviceClasses := make(map[string]*resourcev1.DeviceClass)
	for _, request := range claim.Spec.Devices.Requests {
		if request.Exactly == nil {
			return nil, fmt.Errorf("ResourceClaim request %q is not an exact device request", request.Name)
		}
		className := request.Exactly.DeviceClassName
		if _, resolved := deviceClasses[className]; resolved {
			continue
		}
		deviceClass := &resourcev1.DeviceClass{}
		if err := sb.Get(ctx, client.ObjectKey{Name: className}, deviceClass); err != nil {
			return nil, fmt.Errorf("get DeviceClass %q while binding: %w", className, err)
		}
		deviceClasses[className] = deviceClass
	}
	return deviceClasses, nil
}

func deviceClassAllocationConfigs(claim *resourcev1.ResourceClaim, deviceClasses map[string]*resourcev1.DeviceClass) ([]resourcev1.DeviceAllocationConfiguration, error) {
	type configRange struct {
		start int
		end   int
	}

	configRanges := make(map[string]configRange)
	var configs []resourcev1.DeviceAllocationConfiguration
	for _, request := range claim.Spec.Devices.Requests {
		if request.Exactly == nil {
			return nil, fmt.Errorf("ResourceClaim request %q is not an exact device request", request.Name)
		}
		className := request.Exactly.DeviceClassName
		if existing, added := configRanges[className]; added {
			for i := existing.start; i < existing.end; i++ {
				configs[i].Requests = append(configs[i].Requests, request.Name)
			}
			continue
		}

		deviceClass, ok := deviceClasses[className]
		if !ok {
			return nil, fmt.Errorf("DeviceClass %q was not resolved while binding", className)
		}
		start := len(configs)
		for i := range deviceClass.Spec.Config {
			configuration := deviceClass.Spec.Config[i].DeviceConfiguration.DeepCopy()
			configs = append(configs, resourcev1.DeviceAllocationConfiguration{
				Source:              resourcev1.AllocationConfigSourceClass,
				Requests:            []string{request.Name},
				DeviceConfiguration: *configuration,
			})
		}
		configRanges[className] = configRange{start: start, end: len(configs)}
	}
	return configs, nil
}

func validateDeviceProfileAllocationCounts(allocation *claimAllocation, results []resourcev1.DeviceRequestAllocationResult) error {
	expected := make(map[string]int64)
	if allocation.CoreBitmapAllocation != nil {
		if allocation.CoreBitmapAllocation.CPU == nil {
			return fmt.Errorf("missing CPU allocation")
		}
		for _, request := range allocation.CoreBitmapAllocation.CPU.Requests {
			expected[request.Name] = 1
		}
		if err := validateCPUResults(allocation.CoreBitmapAllocation.CPU, results); err != nil {
			return err
		}
	}
	for _, indexed := range allocation.IndexedGRESAllocations {
		expected[indexed.RequestName] = indexed.Count
	}
	actual := make(map[string]int64, len(expected))
	for _, result := range results {
		if _, profileRequest := expected[result.Request]; profileRequest {
			actual[result.Request]++
		}
	}
	for requestName, expectedCount := range expected {
		if actual[requestName] != expectedCount {
			return fmt.Errorf("DeviceProfile request %q allocated %d devices, expected exactly %d", requestName, actual[requestName], expectedCount)
		}
	}
	return nil
}

func (sb *SlurmBridge) validateDeviceClassRequestsForPods(ctx context.Context, pods []corev1.Pod, exclusive bool) error {
	for i := range pods {
		pod := &pods[i]
		requests, err := sb.registry().ResolvePod(ctx, sb.Client, pod)
		if err != nil {
			return fmt.Errorf("pod %s: %w", klog.KObj(pod), err)
		}
		if err := dra.ValidateRequestContainers(requests); err != nil {
			return fmt.Errorf("pod %s: %w", klog.KObj(pod), err)
		}
		cpu, err := allocateCoreBitmapProfile(requests)
		if err != nil {
			return fmt.Errorf("pod %s: %w", klog.KObj(pod), err)
		}
		if cpu != nil && exclusive {
			return fmt.Errorf("pod %s, DeviceClass %q: %w", klog.KObj(pod), cpu.DeviceClassName, dra.ErrExclusiveCPUAllocation)
		}
	}
	return nil
}

func subsetGRESResources(resources slurmcontrol.NodeResources, requestedCounts map[string]int64, supportedClasses map[string]struct{}) (*slurmcontrol.NodeResources, error) {
	claimResources := resources
	claimResources.Gres = nil
	for _, gres := range resources.Gres {
		count, requested := requestedCounts[gres.Type]
		_, supported := supportedClasses[gres.Type]
		if !requested || !supported {
			continue
		}
		indices, err := hostlist.Expand(fmt.Sprintf("[%s]", gres.Index))
		if err != nil {
			return nil, err
		}
		if count > int64(len(indices)) {
			return nil, fmt.Errorf("not enough allocated Slurm GRES indices for DeviceClass %q: requested %d, allocated %d", gres.Type, count, len(indices))
		}
		gres.Count = count
		gres.Index = strings.Join(indices[:count], ",")
		claimResources.Gres = append(claimResources.Gres, gres)
	}
	return &claimResources, nil
}

func deviceClassNames(requests []resourcev1.DeviceRequest) map[string]struct{} {
	deviceClasses := make(map[string]struct{})
	for _, request := range requests {
		if request.Exactly != nil {
			deviceClasses[request.Exactly.DeviceClassName] = struct{}{}
		}
	}
	return deviceClasses
}

func createContainerRequestMappings(requests []dra.ResourceRequest, deviceRequests []resourcev1.DeviceRequest) []corev1.ContainerExtendedResourceRequest {
	var mappings []corev1.ContainerExtendedResourceRequest
	for _, request := range requests {
		for _, deviceRequest := range deviceRequests {
			if deviceRequest.Exactly != nil && deviceRequest.Exactly.DeviceClassName == request.DeviceClassName {
				mappings = append(mappings, corev1.ContainerExtendedResourceRequest{
					ContainerName: request.ContainerName,
					RequestName:   deviceRequest.Name,
					ResourceName:  string(request.ResourceName),
				})
			}
		}
	}
	return mappings
}

// patchPodExtendedResourceClaimStatus links allocated requests to their containers.
func (sb *SlurmBridge) patchPodExtendedResourceClaimStatus(
	ctx context.Context,
	pod *corev1.Pod,
	claim *resourcev1.ResourceClaim,
	requestMappings []corev1.ContainerExtendedResourceRequest,
) error {
	if len(requestMappings) == 0 {
		return fmt.Errorf("nil or empty request mappings, no update of pod %s/%s ExtendedResourceClaimStatus", pod.Namespace, pod.Name)
	}

	status := &corev1.PodExtendedResourceClaimStatus{
		RequestMappings:   requestMappings,
		ResourceClaimName: claim.Name,
	}
	patch, err := utils.FieldPatch("/status/extendedResourceClaimStatus", status, utils.WithUIDPrecondition(pod.UID))
	if err != nil {
		return fmt.Errorf("encode pod claim status patch: %w", err)
	}
	if err := sb.Status().Patch(ctx, pod.DeepCopy(), patch); err != nil {
		return fmt.Errorf("failed to update pod %s ExtendedResourceClaimStatus: %w", klog.KObj(pod), err)
	}

	return nil
}
