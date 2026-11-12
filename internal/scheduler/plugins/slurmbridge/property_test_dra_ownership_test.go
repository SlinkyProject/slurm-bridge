// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"sync"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Vary the full device identity and access mode. Shared names across namespaces,
// drivers, and pools must neither hide conflicts nor cause false conflicts.
// Concurrent calls share one bridge instance, as in a scheduler process.
func TestSchedulerDRAOwnershipProperties(t *testing.T) {
	t.Parallel()
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent=%t", concurrent), func(t *testing.T) {
			t.Parallel()
			rapid.Check(t, func(rt *rapid.T) {
				sb := claimWriter(interceptor.Funcs{})
				n := rapid.IntRange(2, 16).Draw(rt, "claims")
				claims, allocated := make([]*resourcev1.ResourceClaim, n), make([]*resourcev1.ResourceClaim, n)
				for i := range n {
					claims[i] = &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%d", i/2), Namespace: fmt.Sprintf("ns-%d", i%2), UID: types.UID(fmt.Sprintf("uid-%d", i))}}
					if err := sb.Create(rt.Context(), claims[i]); err != nil {
						rt.Fatal(err)
					}
					allocated[i] = claims[i].DeepCopy()
					ids := rapid.SliceOfNDistinct(rapid.IntRange(0, 7), 1, 4, func(n int) int { return n }).Draw(rt, "devices")
					for _, id := range ids {
						result := resourcev1.DeviceRequestAllocationResult{Request: fmt.Sprintf("request-%d", id), Driver: fmt.Sprintf("driver%d.example.com", id/4), Pool: fmt.Sprintf("pool-%d", id/2%2), Device: fmt.Sprintf("device-%d", id%2)}
						switch rapid.SampledFrom([]string{"exclusive", "shared", "admin"}).Draw(rt, "access") {
						case "shared":
							result.ShareID = ptr.To(types.UID(fmt.Sprintf("share-%d-%d", i, id)))
						case "admin":
							result.AdminAccess = ptr.To(true)
						}
						if allocated[i].Status.Allocation == nil {
							allocated[i].Status.Allocation = &resourcev1.AllocationResult{}
						}
						allocated[i].Status.Allocation.Devices.Results = append(allocated[i].Status.Allocation.Devices.Results, result)
					}
				}
				read := func() []resourcev1.ResourceClaim {
					list := &resourcev1.ResourceClaimList{}
					if err := sb.List(rt.Context(), list); err != nil {
						rt.Fatal(err)
					}
					return list.Items
				}
				errs := make([]error, n)
				order := rapid.Permutation(schedulingPropertyIndices(n)).Draw(rt, "binding-order")
				if concurrent {
					var wg sync.WaitGroup
					start := make(chan struct{})
					for _, i := range order {
						wg.Go(func() { <-start; errs[i] = sb.patchClaimAllocation(rt.Context(), claims[i], allocated[i]) })
					}
					close(start)
					wg.Wait()
				} else {
					for _, i := range order {
						candidate := append(read(), *allocated[i])
						want := checkDRAOwnership(draPropertyState{claims: candidate}) == nil
						errs[i] = sb.patchClaimAllocation(rt.Context(), claims[i], allocated[i])
						if (errs[i] == nil) != want {
							rt.Fatalf("ownership decision=%v, compatible=%t", errs[i], want)
						}
					}
				}
				if len(sb.uncertainClaims) != 0 {
					rt.Fatal("successful writes retained uncertain reservations")
				}
				stored := read()
				if err := checkDRAOwnership(draPropertyState{claims: stored}); err != nil {
					rt.Fatal(err)
				}
				successes := 0
				for i, err := range errs {
					if err == nil {
						successes++
						continue
					}
					// With no releases, every rejected write must conflict with a final owner.
					if checkDRAOwnership(draPropertyState{claims: append(stored, *allocated[i])}) == nil {
						rt.Fatalf("rejected compatible allocation: %v", err)
					}
				}
				if successes == 0 {
					rt.Fatal("no contender made progress")
				}
				for i := range stored {
					stored[i].Status = resourcev1.ResourceClaimStatus{}
					if err := sb.Status().Update(rt.Context(), &stored[i]); err != nil {
						rt.Fatal(err)
					}
				}
				winner := order[0]
				if err := sb.Get(rt.Context(), client.ObjectKeyFromObject(claims[winner]), claims[winner]); err != nil {
					rt.Fatal(err)
				}
				allocated[winner].ResourceVersion = claims[winner].ResourceVersion
				if err := sb.patchClaimAllocation(rt.Context(), claims[winner], allocated[winner]); err != nil {
					rt.Fatalf("release stranded devices: %v", err)
				}
			})
		})
	}
}

func TestSchedulerDRAUncertainReassignmentProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		fail := true
		sb := claimWriter(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if fail {
				return fmt.Errorf("timeout before server completion")
			}
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}})
		claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "default", UID: "claim-uid"}}
		if err := sb.Create(rt.Context(), claim); err != nil {
			rt.Fatal(err)
		}
		target := claim.DeepCopy()
		device := rapid.IntRange(0, 15).Draw(rt, "old-device")
		target.Status.Allocation = &resourcev1.AllocationResult{Devices: resourcev1.DeviceAllocationResult{Results: []resourcev1.DeviceRequestAllocationResult{{Driver: "gpu.example.com", Pool: "node", Device: fmt.Sprintf("gpu-%d", device)}}}}
		if err := sb.patchClaimAllocation(rt.Context(), claim, target); err == nil {
			rt.Fatal("expected timeout")
		}
		fail = false
		target.Status.Allocation.Devices.Results[0].Device = fmt.Sprintf("gpu-%d", device+1)
		if err := sb.patchClaimAllocation(rt.Context(), claim, target); err == nil {
			rt.Fatal("changed devices before old write was fenced")
		}
		claim.Annotations = map[string]string{"fence": "true"}
		if err := sb.Update(rt.Context(), claim); err != nil {
			rt.Fatal(err)
		}
		target.ResourceVersion = claim.ResourceVersion
		if err := sb.patchClaimAllocation(rt.Context(), claim, target); err != nil {
			rt.Fatalf("fenced write blocked reassignment: %v", err)
		}
	})
}
