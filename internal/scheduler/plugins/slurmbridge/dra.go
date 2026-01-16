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

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/util/slice"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
)

// manageResourceClaim will create DRA ResourceClaims for each
// Slurm GRES type that matches a DRA DeviceClass name. Additionally,
// if the CPU DRA driver is installed, A ResourceClaim for CPUs will
// be generated. The ResourceClaim is reconstructed in a similar manner
// to the way the Kubernetes scheduler handles the DRA Extended Resource
// Claim capability.
func (sb *SlurmBridge) manageResourceClaim(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) error {
	claim, requestMappings, err := sb.createRequestsAndMappings(ctx, pod, nodeName, resources)
	if err != nil {
		return err
	}
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

func (sb *SlurmBridge) createRequestsAndMappings(ctx context.Context, pod *corev1.Pod, nodeName string, resources *slurmcontrol.NodeResources) (*resourcev1.ResourceClaim, []corev1.ContainerExtendedResourceRequest, error) {
	if pod == nil {
		return nil, nil, errors.New("expected a pod to be given")
	}

	containers := slices.Clone(pod.Spec.InitContainers)
	containers = append(containers, pod.Spec.Containers...)

	// all mappings across all containers and resource types
	var mappings []corev1.ContainerExtendedResourceRequest

	nodeInfo, err := nodeinfo.NewNodeInfo(ctx, sb.Client, nodeName)
	if err != nil {
		return nil, nil, err
	}

	deviceRequests, err := nodeInfo.GetDeviceRequests(ctx, sb.Client, resources)
	if err != nil {
		return nil, nil, err
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
			if rName.String() == corev1.ResourceCPU.String() {
				reqMap := corev1.ContainerExtendedResourceRequest{
					ContainerName: container.Name,
					RequestName:   reqName,
					ResourceName:  corev1.ResourceCPU.String(),
				}
				mappings = append(mappings, reqMap)
				continue
			}
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

	return claim, mappings, nil
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

	devices, err := nodeInfo.GetDeviceRequestAllocationResult(ctx, sb.Client, resources)
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
