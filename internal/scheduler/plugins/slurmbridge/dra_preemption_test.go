// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/testutils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Reproduce #58 at the claim-allocation boundary: Slurm has reassigned GPU 0
// to B, but A is still Running during its termination grace period. A's claim
// must prevent B from receiving the same device until the allocation is released.
func TestSlurmBridge_bindClaim_PreemptedDevice(t *testing.T) {
	for _, tc := range []struct {
		name         string
		oldNamespace string
		released     bool
	}{
		{name: "terminating pod still owns device", oldNamespace: "serve"},
		{name: "terminating pod in another namespace still owns device", oldNamespace: "batch"},
		{name: "released allocation permits binding", oldNamespace: "batch", released: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			device := resourcev1.DeviceRequestAllocationResult{
				Request: "gpu", Driver: legacyDRAExampleDriver, Pool: "node1", Device: "gpu-0",
			}
			oldPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: tc.oldNamespace, Name: "preempted", UID: "preempted-uid",
					DeletionTimestamp: ptr.To(metav1.Now()),
					Finalizers:        []string{wellknown.FinalizerScheduler},
				},
				Spec: corev1.PodSpec{NodeName: "node1", TerminationGracePeriodSeconds: ptr.To[int64](60)},
				Status: corev1.PodStatus{
					Phase:                       corev1.PodRunning,
					ExtendedResourceClaimStatus: &corev1.PodExtendedResourceClaimStatus{ResourceClaimName: "old-claim"},
				},
			}
			oldClaim := &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: oldPod.Namespace, Name: "old-claim", UID: "old-claim-uid",
					Finalizers: []string{resourcev1.Finalizer},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "v1", Kind: "Pod", Name: oldPod.Name, UID: oldPod.UID,
					}},
				},
				Status: resourcev1.ResourceClaimStatus{
					Allocation: &resourcev1.AllocationResult{
						Devices: resourcev1.DeviceAllocationResult{Results: []resourcev1.DeviceRequestAllocationResult{device}},
					},
					ReservedFor: []resourcev1.ResourceClaimConsumerReference{{Resource: "pods", Name: oldPod.Name, UID: oldPod.UID}},
				},
			}
			if tc.released {
				oldPod.Status.Phase = corev1.PodSucceeded
				oldClaim.Status = resourcev1.ResourceClaimStatus{}
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "serve", Name: "preemptor", UID: "preemptor-uid"}}
			claim := &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: "new-claim", UID: "new-claim-uid"},
				Spec: resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{Requests: []resourcev1.DeviceRequest{{
					Name: "gpu", Exactly: &resourcev1.ExactDeviceRequest{
						DeviceClassName: legacyDRAExampleDriver, AllocationMode: resourcev1.DeviceAllocationModeExactCount, Count: 1,
					},
				}}}},
			}
			slice := &resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "node1-gpus"},
				Spec: resourcev1.ResourceSliceSpec{
					NodeName: ptr.To("node1"), Driver: device.Driver,
					Pool: resourcev1.ResourcePool{Name: device.Pool, Generation: 1, ResourceSliceCount: 1},
					Devices: []resourcev1.Device{{Name: device.Device, Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						legacyDRAExampleGPUIndexAttribute: {IntValue: ptr.To[int64](0)},
					}}},
				},
			}
			kube := fake.NewClientBuilder().
				WithObjects(oldPod, oldClaim, pod, claim, slice,
					&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: legacyDRAExampleDriver}}).
				WithStatusSubresource(&resourcev1.ResourceClaim{}).Build()
			sb := &SlurmBridge{Client: kube, draRegistry: testutils.DRARegistryWithExampleGPU()}
			if err := kube.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
				t.Fatal(err)
			}
			err := sb.bindClaim(ctx, claim, pod, "node1", &claimAllocation{NodeResources: &slurmcontrol.NodeResources{
				Node: "node1", Gres: []slurmcontrol.GresLayout{{Name: "gpu", Type: legacyDRAExampleDriver, Count: 1, Index: "0"}},
			}})
			stored := &resourcev1.ResourceClaim{}
			if getErr := kube.Get(ctx, client.ObjectKeyFromObject(claim), stored); getErr != nil {
				t.Fatal(getErr)
			}
			if tc.released {
				if err != nil {
					t.Fatalf("bindClaim() after release: %v", err)
				}
				if stored.Status.Allocation == nil || !reflect.DeepEqual(stored.Status.Allocation.Devices.Results, []resourcev1.DeviceRequestAllocationResult{device}) {
					t.Fatalf("expected GPU 0 allocation after release, got %+v", stored.Status.Allocation)
				}
				return
			}
			if err == nil {
				t.Error("bindClaim() succeeded while the terminating pod still owns GPU 0; expected a retryable error")
			}
			remaining := &resourcev1.ResourceClaim{}
			if getErr := kube.Get(ctx, client.ObjectKeyFromObject(oldClaim), remaining); getErr != nil {
				t.Fatal(getErr)
			}
			if !reflect.DeepEqual(remaining.Status, oldClaim.Status) {
				t.Fatal("binding the preemptor changed the old claim's allocation or reservation")
			}
			if stored.Status.Allocation != nil {
				t.Errorf("new claim received devices still allocated to %s/%s: %+v", oldClaim.Namespace, oldClaim.Name, stored.Status.Allocation.Devices.Results)
			}
			if len(stored.Status.ReservedFor) != 0 {
				t.Error("conflicting claim was reserved for the preemptor")
			}
		})
	}
}
