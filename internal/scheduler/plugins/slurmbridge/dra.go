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

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	schedutil "k8s.io/kubernetes/pkg/scheduler/util"
	"k8s.io/utils/ptr"
)

// createResourceClaim will create DRA ResouceClaims for each
// Slurm Gres type that matches a DRA DeviceClass name. The
// ResourceClaim is reconstructed in a similar manner to the
// way the Kubernetes scheduler handles the DRA Extended Resource
// Claim capability.
//
// As Slurm has told the slurm-bridge scheduler the Gres resources
// are available for the pods to use the created ResouceClaim will
// be initialized as allocated and reserved.
func (sb *SlurmBridge) createResourceClaim(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) error {

	var deviceRequests []resourcev1.DeviceRequest
	for _, gres := range resources.Gres {
		if !sb.validateDeviceClass(ctx, gres.Type) {
			continue
		}
		request := deviceRequest(gres.Name, gres.Type, gres.Count)
		deviceRequests = append(deviceRequests, request)
	}

	// If the placeholderJob has no GRES allocations, there are no ResourceClaims to create
	if len(deviceRequests) == 0 {
		return nil
	}

	// Initializing a ResourceClaim struct requires that
	// claim.Name, claim.UID, and claim.Status.Allocation
	// are not set initially.
	claim := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    pod.Namespace,
			Name:         "",
			UID:          "",
			GenerateName: pod.Name,
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
	var err error
	claim, err = sb.handle.ClientSet().ResourceV1().ResourceClaims(claim.Namespace).Create(ctx, claim, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create claim for extended resources %v: %w", klog.KObj(claim), err)
	}

	claim.Status.Allocation = sb.getAllocationResult(ctx, nodeName, resources)
	claim.Status.ReservedFor = []resourcev1.ResourceClaimConsumerReference{
		{Resource: "pods", Name: pod.Name, UID: pod.UID},
	}

	claim, err = sb.handle.ClientSet().ResourceV1().ResourceClaims(claim.Namespace).UpdateStatus(ctx, claim, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("add reservation to claim %s: %w", klog.KObj(claim), err)
	}

	// Patch the pod status with the new information about the generated
	// special resource claim.
	podStatusCopy := pod.Status.DeepCopy()
	podStatusCopy.ExtendedResourceClaimStatus = &corev1.PodExtendedResourceClaimStatus{
		RequestMappings:   generateRequestMappings(pod, resources),
		ResourceClaimName: claim.Name,
	}
	err = schedutil.PatchPodStatus(ctx, sb.handle.ClientSet(), pod.Name, pod.Namespace, &pod.Status, podStatusCopy)
	if err != nil {
		return fmt.Errorf("update pod %s/%s ExtendedResourceClaimStatus: %w", pod.Namespace, pod.Name, err)
	}

	return nil
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

func generateRequestMappings(pod *corev1.Pod, resources *slurmcontrol.NodeResources) []corev1.ContainerExtendedResourceRequest {
	containers := slices.Clone(pod.Spec.InitContainers)
	containers = append(containers, pod.Spec.Containers...)
	requestMappings := []corev1.ContainerExtendedResourceRequest{}
	for _, c := range containers {
		for req := range c.Resources.Requests {
			for _, dev := range resources.Gres {
				if !strings.HasSuffix(req.String(), dev.Type) {
					continue
				}
				reqMap := corev1.ContainerExtendedResourceRequest{
					ContainerName: c.Name,
					RequestName:   dev.Name,
					ResourceName:  resourcev1.ResourceDeviceClassPrefix + dev.Type,
				}
				requestMappings = append(requestMappings, reqMap)
			}
		}
	}
	return requestMappings
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
