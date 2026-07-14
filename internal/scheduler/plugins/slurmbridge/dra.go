// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2024 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/puttsk/hostlist"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

// manageResourceClaim will create DRA ResourceClaims for each
// Slurm GRES type that matches a DRA DeviceClass name. Additionally,
// a ResourceClaim for CPUs will be generated when the pod explicitly
// requests the CPU DRA extended resource.
func (sb *SlurmBridge) manageResourceClaim(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) error {
	claim, requestMappings, claimResources, err := sb.createRequestsAndMappings(ctx, pod, nodeName, resources)
	if err != nil {
		return err
	}
	if claim == nil || requestMappings == nil || claimResources == nil {
		return nil
	}

	if err := sb.Create(ctx, claim); err != nil {
		var errs []error
		errs = append(errs, fmt.Errorf("create claim for extended resources %v: %w", klog.KObj(claim), err))

		if deleteErr := sb.Delete(ctx, claim); deleteErr != nil {
			errs = append(errs, fmt.Errorf("delete claim for extended resources %v: %w", klog.KObj(claim), deleteErr))
		}

		return utilerrors.NewAggregate(errs)
	}

	if err := sb.bindClaim(ctx, claim, pod, nodeName, claimResources); err != nil {
		var errs []error
		errs = append(errs, err)

		if deleteErr := sb.Delete(ctx, claim); deleteErr != nil {
			errs = append(errs, fmt.Errorf("delete claim for extended resources %v: %w", klog.KObj(claim), deleteErr))
		}

		return utilerrors.NewAggregate(errs)
	}

	if err := sb.patchPodExtendedResourceClaimStatus(ctx, pod, claim, requestMappings); err != nil {
		var errs []error
		errs = append(errs, err)

		if deleteErr := sb.Delete(ctx, claim); deleteErr != nil {
			errs = append(errs, fmt.Errorf("delete claim for extended resources %v: %w", klog.KObj(claim), deleteErr))
		}

		return utilerrors.NewAggregate(errs)
	}

	return nil
}

func (sb *SlurmBridge) createRequestsAndMappings(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) (*resourcev1.ResourceClaim, []corev1.ContainerExtendedResourceRequest, *slurmcontrol.NodeResources, error) {
	if pod == nil {
		return nil, nil, nil, errors.New("expected a pod to be given")
	}

	nodeInfo, err := nodeinfo.NewNodeInfo(ctx, sb.Client, nodeName)
	if err != nil {
		return nil, nil, nil, err
	}

	podRequestsCPUDRA := podRequestsCPUDRAExtendedResource(pod)
	allocatedRequests, err := nodeInfo.GetDeviceRequests(ctx, sb.Client, resources, podRequestsCPUDRA)
	if err != nil {
		return nil, nil, nil, err
	}
	claimIncludesCPUDRARequest := hasDeviceRequestNamed(allocatedRequests, corev1.ResourceCPU.String())
	if podRequestsCPUDRA && !claimIncludesCPUDRARequest {
		return nil, nil, nil, fmt.Errorf("pod requests CPU DRA resource %q but no CPU device request was generated", nodeinfo.DraDriverCpu_ExtendedResourceName)
	}

	if resources == nil {
		return nil, nil, nil, errors.New("expected node resources")
	}
	claimResources, err := subsetGRESResources(*resources, deviceClassRequestCounts(pod), deviceClassNames(allocatedRequests))
	if err != nil {
		return nil, nil, nil, err
	}

	deviceRequests, err := nodeInfo.GetDeviceRequests(ctx, sb.Client, claimResources, podRequestsCPUDRA)
	if err != nil {
		return nil, nil, nil, err
	}

	mappings, err := createContainerRequestMappings(pod, deviceRequests)
	if err != nil {
		return nil, nil, nil, err
	}

	if len(deviceRequests) == 0 || len(mappings) == 0 {
		return nil, nil, nil, nil
	}

	claim := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    pod.Namespace,
			GenerateName: pod.Name + "-extended-resources-",
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
			},
		},
		Spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: deviceRequests,
			},
		},
	}

	return claim, mappings, claimResources, nil
}

// bindClaim gets called for claims which are not reserved for the pod yet.
// It might not even be allocated. bindClaim then ensures that the allocation
// and reservation are recorded.
func (sb *SlurmBridge) bindClaim(
	ctx context.Context,
	claim *resourcev1.ResourceClaim,
	pod *corev1.Pod,
	nodeName string,
	resources *slurmcontrol.NodeResources,
) error {
	nodeInfo, err := nodeinfo.NewNodeInfo(ctx, sb.Client, nodeName)
	if err != nil {
		return err
	}

	claimIncludesCPUDRARequest := claimRequestsCPUDRA(claim)
	devices, err := nodeInfo.GetDeviceRequestAllocationResult(ctx, sb.Client, resources, claimIncludesCPUDRARequest)
	if err != nil {
		return err
	}

	toUpdate := claim.DeepCopy()

	toUpdate.Status.Allocation = &resourcev1.AllocationResult{
		AllocationTimestamp: &metav1.Time{
			Time: time.Now(),
		},
		Devices: resourcev1.DeviceAllocationResult{
			Results: devices,
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

	toUpdate.Status.ReservedFor = []resourcev1.ResourceClaimConsumerReference{
		{Resource: "pods", Name: pod.Name, UID: pod.UID},
	}

	if err := sb.Status().Patch(ctx, toUpdate, client.StrategicMergeFrom(claim)); err != nil {
		return fmt.Errorf("failed to add reservation to claim %s status: %w", klog.KObj(claim), err)
	}

	if err := sb.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
		return fmt.Errorf("failed to get claim %s: %w", klog.KObj(claim), err)
	}

	return nil
}

func validateDeviceClassRequests(pod *corev1.Pod) error {
	requestingContainer := make(map[string]string)
	containers := slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers)
	for _, container := range containers {
		for resourceName, quantity := range container.Resources.Requests {
			className, ok := strings.CutPrefix(resourceName.String(), resourcev1.ResourceDeviceClassPrefix)
			if !ok || className == nodeinfo.DraDriverCpu || quantity.Value() <= 0 {
				continue
			}
			if existing, ok := requestingContainer[className]; ok && existing != container.Name {
				return fmt.Errorf("DRA DeviceClass %q is requested by multiple containers %q and %q; slurm-bridge currently supports one requesting container per DeviceClass", className, existing, container.Name)
			}
			requestingContainer[className] = container.Name
		}
	}
	return nil
}

func validateDeviceClassRequestsForPods(pods []corev1.Pod) error {
	for i := range pods {
		pod := &pods[i]
		if err := validateDeviceClassRequests(pod); err != nil {
			return fmt.Errorf("pod %s: %w", klog.KObj(pod), err)
		}
	}
	return nil
}

func deviceClassRequestCounts(pod *corev1.Pod) map[string]int64 {
	counts := make(map[string]int64)
	containers := slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers)
	for _, container := range containers {
		for resourceName, quantity := range container.Resources.Requests {
			className, ok := strings.CutPrefix(resourceName.String(), resourcev1.ResourceDeviceClassPrefix)
			if !ok || className == nodeinfo.DraDriverCpu || quantity.Value() <= 0 {
				continue
			}
			counts[className] = quantity.Value()
		}
	}
	return counts
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
		if request.Exactly != nil && request.Exactly.DeviceClassName != nodeinfo.DraDriverCpu {
			deviceClasses[request.Exactly.DeviceClassName] = struct{}{}
		}
	}
	return deviceClasses
}

func createContainerRequestMappings(pod *corev1.Pod, deviceRequests []resourcev1.DeviceRequest) ([]corev1.ContainerExtendedResourceRequest, error) {
	containers := slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers)

	requestNames := make(map[string]string, len(deviceRequests))
	for _, request := range deviceRequests {
		if request.Exactly == nil {
			continue
		}
		className := request.Exactly.DeviceClassName
		if existing, ok := requestNames[className]; ok && existing != request.Name {
			return nil, fmt.Errorf("multiple DRA requests for DeviceClass %q are not supported", className)
		}
		requestNames[className] = request.Name
	}

	var mappings []corev1.ContainerExtendedResourceRequest
	for _, container := range containers {
		keys := make([]string, 0, len(container.Resources.Requests))
		for resourceName := range container.Resources.Requests {
			keys = append(keys, resourceName.String())
		}
		slices.Sort(keys)
		for _, resourceName := range keys {
			quantity := container.Resources.Requests[corev1.ResourceName(resourceName)]
			if quantity.Value() <= 0 {
				continue
			}

			className := strings.TrimPrefix(resourceName, resourcev1.ResourceDeviceClassPrefix)
			if resourceName == nodeinfo.DraDriverCpu_ExtendedResourceName {
				className = nodeinfo.DraDriverCpu
			} else if className == resourceName {
				continue
			}

			requestName, ok := requestNames[className]
			if !ok {
				continue
			}
			mappings = append(mappings, corev1.ContainerExtendedResourceRequest{
				ContainerName: container.Name,
				RequestName:   requestName,
				ResourceName:  resourceName,
			})
		}
	}

	return mappings, nil
}

func podRequestsCPUDRAExtendedResource(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	containers := slices.Clone(pod.Spec.InitContainers)
	containers = append(containers, pod.Spec.Containers...)
	for _, container := range containers {
		if quantity, ok := container.Resources.Requests[corev1.ResourceName(nodeinfo.DraDriverCpu_ExtendedResourceName)]; ok && !quantity.IsZero() {
			return true
		}
	}
	return false
}

func hasDeviceRequestNamed(requests []resourcev1.DeviceRequest, name string) bool {
	for _, request := range requests {
		if request.Name == name {
			return true
		}
	}
	return false
}

func claimRequestsCPUDRA(claim *resourcev1.ResourceClaim) bool {
	if claim == nil {
		return false
	}
	for _, req := range claim.Spec.Devices.Requests {
		if req.Name == corev1.ResourceCPU.String() && req.Exactly != nil && req.Exactly.DeviceClassName == nodeinfo.DraDriverCpu {
			return true
		}
	}
	return false
}

// patchPodExtendedResourceClaimStatus updates the pod's status with information about
// the extended resource claim.
func (sb *SlurmBridge) patchPodExtendedResourceClaimStatus(
	ctx context.Context,
	pod *corev1.Pod,
	claim *resourcev1.ResourceClaim,
	requestMappings []corev1.ContainerExtendedResourceRequest,
) error {
	if len(requestMappings) == 0 {
		return fmt.Errorf("nil or empty request mappings, no update of pod %s/%s ExtendedResourceClaimStatus", pod.Namespace, pod.Name)
	}

	toUpdate := pod.DeepCopy()
	toUpdate.Status.ExtendedResourceClaimStatus = &corev1.PodExtendedResourceClaimStatus{
		RequestMappings:   requestMappings,
		ResourceClaimName: claim.Name,
	}
	if err := sb.Status().Patch(ctx, toUpdate, client.StrategicMergeFrom(pod)); err != nil {
		return fmt.Errorf("failed to update pod %s ExtendedResourceClaimStatus: %w", klog.KObj(pod), err)
	}

	if err := sb.Get(ctx, client.ObjectKeyFromObject(toUpdate), toUpdate); err != nil {
		return fmt.Errorf("failed to get pod %s: %w", klog.KObj(pod), err)
	}

	return nil
}
