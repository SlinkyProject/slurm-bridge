// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

type upgradeGPUAllocation struct {
	claimUID types.UID
	driver   string
	pool     string
	device   string
}

func addUpgradeGPURequest(pod *corev1.Pod) {
	resources := &pod.Spec.Containers[0].Resources
	resources.Requests[draNvidiaGPUResource] = resource.MustParse("1")
	if resources.Limits == nil {
		resources.Limits = corev1.ResourceList{}
	}
	resources.Limits[draNvidiaGPUResource] = resource.MustParse("1")
}

func observeUpgradeGPU(ctx context.Context, t *testing.T, config *envconf.Config, c client.Client, pod *corev1.Pod) upgradeGPUAllocation {
	t.Helper()
	status := pod.Status.ExtendedResourceClaimStatus
	if status == nil || status.ResourceClaimName == "" {
		t.Fatalf("GPU pod %s has no ResourceClaim", pod.Name)
	}
	claim := &resourcev1.ResourceClaim{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: status.ResourceClaimName}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Status.Allocation == nil || len(claim.Status.Allocation.Devices.Results) != 1 {
		t.Fatalf("GPU claim %s must allocate one device: %s", claim.Name, statusJSON(claim.Status))
	}
	allocated := claim.Status.Allocation.Devices.Results[0]
	if allocated.Driver != draNvidiaGPUDriver {
		t.Fatalf("GPU claim uses driver %s, want %s", allocated.Driver, draNvidiaGPUDriver)
	}
	devices, err := execInPod(ctx, config, pod, "sh", "-c",
		`for device in /dev/nvidia[0-9]*; do [ ! -e "$device" ] || echo "$device"; done`)
	if err != nil {
		t.Fatal(err)
	}
	if count := len(strings.Fields(devices)); count != 1 {
		t.Fatalf("GPU pod %s exposes %d NVIDIA device nodes, want 1: %q", pod.Name, count, devices)
	}
	return upgradeGPUAllocation{claimUID: claim.UID, driver: allocated.Driver, pool: allocated.Pool, device: allocated.Device}
}
