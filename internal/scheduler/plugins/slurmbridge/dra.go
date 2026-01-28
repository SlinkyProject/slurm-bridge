// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2024 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	schedutil "k8s.io/kubernetes/pkg/scheduler/util"
	"k8s.io/kubernetes/pkg/util/slice"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

// preBindExtendedResources will create DRA ResouceClaims for each
// Slurm Gres type that matches a DRA DeviceClass name. The
// ResourceClaim is reconstructed in a similar manner to the
// way the Kubernetes scheduler handles the DRA Extended Resource
// Claim capability.
//
// As Slurm has told the slurm-bridge scheduler the Gres resources
// are available for the pods to use the created ResouceClaim will
// be initialized as allocated and reserved.
func (sb *SlurmBridge) preBindExtendedResources(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) error {
	claim, requestMappings := sb.createRequestsAndMappings(ctx, pod, resources)
	if claim == nil || requestMappings == nil {
		return nil
	}

	if err := sb.Create(ctx, claim); err != nil {
		return fmt.Errorf("create claim for extended resources %v: %w", klog.KObj(claim), err)
	}

	if err := sb.bindClaim(ctx, claim, pod, nodeName, resources); err != nil {
		return err
	}

	if err := sb.patchPodExtendedResourceClaimStatus(ctx, pod, claim, requestMappings); err != nil {
		return err
	}

	return nil
}

func (sb *SlurmBridge) createRequestsAndMappings(ctx context.Context, pod *corev1.Pod, resources *slurmcontrol.NodeResources) (*resourcev1.ResourceClaim, []corev1.ContainerExtendedResourceRequest) {
	containers := slices.Clone(pod.Spec.InitContainers)
	containers = append(containers, pod.Spec.Containers...)

	// all requests across all containers and resource types
	var deviceRequests []resourcev1.DeviceRequest
	// all mappings across all containers and resource types
	var mappings []corev1.ContainerExtendedResourceRequest

	for _, gres := range resources.Gres {
		if !sb.validateDeviceClass(ctx, gres.Type) {
			continue
		}
		deviceRequest := deviceRequest(gres.Name, gres.Type, gres.Count)
		deviceRequests = append(deviceRequests, deviceRequest)
	}

	for containerIndex, container := range containers {
		creqs := container.Resources.Requests
		keys := make([]string, 0, len(creqs))
		for k := range creqs {
			keys = append(keys, k.String())
		}
		// resource requests in a container is a map, their names must
		// be sorted to determine the resource's index order.
		slice.SortStrings(keys)
		for rName := range creqs {
			ridx := 0
			for j := range keys {
				if keys[j] == rName.String() {
					ridx = j
					break
				}
			}
			// containerIndex is the index of the container in the list of initContainers + containers.
			// ridx is the index of the extended resource request in the sorted all requests in the container.
			// crq is the quantity of the extended resource request.
			reqName := fmt.Sprintf("container-%d-request-%d", containerIndex, ridx)
			for _, gres := range resources.Gres {
				if !strings.HasSuffix(rName.String(), gres.Type) {
					continue
				}
				reqMap := corev1.ContainerExtendedResourceRequest{
					ContainerName: container.Name,
					RequestName:   reqName,
					ResourceName:  resourcev1.ResourceDeviceClassPrefix + gres.Type,
				}
				mappings = append(mappings, reqMap)
			}
		}
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

	return claim, mappings
}

func deviceRequest(name, deviceClassName string, count int64) resourcev1.DeviceRequest {
	return resourcev1.DeviceRequest{
		Name: name,
		Exactly: &resourcev1.ExactDeviceRequest{
			DeviceClassName: deviceClassName,
			AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
			Count:           count,
		},
	}
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
	claim.Status.Allocation = sb.getAllocationResult(ctx, nodeName, resources)
	claim.Status.ReservedFor = []resourcev1.ResourceClaimConsumerReference{
		{Resource: "pods", Name: pod.Name, UID: pod.UID},
	}

	if err := sb.Status().Update(ctx, claim); err != nil {
		return fmt.Errorf("add reservation to claim %s: %w", klog.KObj(claim), err)
	}

	return nil
}

func (sb *SlurmBridge) getAllocationResult(ctx context.Context, nodeName string, resources *slurmcontrol.NodeResources) *resourcev1.AllocationResult {
	var devices []resourcev1.DeviceRequestAllocationResult

	for _, gres := range resources.Gres {
		if !sb.validateDeviceClass(ctx, gres.Type) {
			continue
		}
		expandedDevices, _ := expandDevices(gres.Count, gres.Index)
		for _, i := range expandedDevices {
			dev := resourcev1.DeviceRequestAllocationResult{
				Request: gres.Name,
				Driver:  gres.Type,
				Pool:    nodeName,
				Device:  gres.Name + "-" + i,
			}
			devices = append(devices, dev)
		}
	}

	res := &resourcev1.AllocationResult{
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
	return res
}

// expand devices translates an index string into an expanded []string
func expandDevices(gresCount int64, gresIndexes string) ([]string, error) {
	indexes := make([]string, gresCount)
	if gresIndexes == "" {
		for i := range gresCount {
			indexes[i] = fmt.Sprintf("%d", i)
		}
		return indexes, nil
	}
	elements := strings.Split(gresIndexes, ",")
	i := 0
	for _, index := range elements {
		if !strings.Contains(index, "-") {
			indexes[i] = index
			i++
		} else {
			bounds := strings.Split(index, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("invalid range: %s", index)
			}
			lower, _ := strconv.Atoi(bounds[0])
			upper, _ := strconv.Atoi(bounds[1])
			for j := lower; j <= upper; j++ {
				indexes[i] = strconv.Itoa(j)
				i++
			}
		}
	}
	return indexes, nil
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

	podStatusCopy := pod.Status.DeepCopy()
	podStatusCopy.ExtendedResourceClaimStatus = &corev1.PodExtendedResourceClaimStatus{
		RequestMappings:   requestMappings,
		ResourceClaimName: claim.Name,
	}
	err := schedutil.PatchPodStatus(ctx, sb.handle.ClientSet(), pod.Name, pod.Namespace, &pod.Status, podStatusCopy)
	if err != nil {
		return fmt.Errorf("update pod %s/%s ExtendedResourceClaimStatus: %w", pod.Namespace, pod.Name, err)
	}

	return nil
}

// Verify a GRES type exists for the DeviceClass
func (sb *SlurmBridge) validateDeviceClass(ctx context.Context, deviceClassName string) bool {
	if deviceClassName == "" {
		return false
	}
	_, err := sb.handle.ClientSet().ResourceV1().DeviceClasses().Get(ctx, deviceClassName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		return false
	}
	return true
}
