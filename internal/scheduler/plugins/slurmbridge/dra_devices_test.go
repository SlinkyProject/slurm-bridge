// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func claimWriter(funcs interceptor.Funcs) *SlurmBridge {
	return &SlurmBridge{Client: fake.NewClientBuilder().WithGlobalResourceVersionCounter().
		WithStatusSubresource(&resourcev1.ResourceClaim{}).WithInterceptorFuncs(funcs).Build()}
}

func deviceClaim(t *testing.T, sb *SlurmBridge, name string, shared bool) (*resourcev1.ResourceClaim, *resourcev1.ResourceClaim) {
	t.Helper()
	claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: name, UID: types.UID(name)}}
	if err := sb.Create(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	allocated := claim.DeepCopy()
	result := resourcev1.DeviceRequestAllocationResult{Driver: "gpu.example.com", Pool: "node", Device: "gpu-0"}
	if shared {
		result.ShareID = ptr.To(types.UID(name))
	}
	allocated.Status.Allocation = &resourcev1.AllocationResult{Devices: resourcev1.DeviceAllocationResult{Results: []resourcev1.DeviceRequestAllocationResult{result}}}
	return claim, allocated
}

func releaseDeviceClaim(t *testing.T, sb *SlurmBridge, claim *resourcev1.ResourceClaim) {
	t.Helper()
	if err := sb.Get(t.Context(), client.ObjectKeyFromObject(claim), claim); err != nil {
		t.Fatal(err)
	}
	claim.Status = resourcev1.ResourceClaimStatus{}
	if err := sb.Status().Update(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
}

func TestClaimDevicesPatchFailure(t *testing.T) {
	resource := schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"}
	for _, tc := range []struct {
		name      string
		err       error
		uncertain bool
	}{
		{"not found", apierrors.NewNotFound(resource, "a"), false},
		{"conflict", apierrors.NewConflict(resource, "a", fmt.Errorf("conflict")), false},
		{"invalid", apierrors.NewInvalid(schema.GroupKind{Group: resource.Group, Kind: "ResourceClaim"}, "a", nil), false},
		{"forbidden", apierrors.NewForbidden(resource, "a", fmt.Errorf("forbidden")), false},
		{"unauthorized", apierrors.NewUnauthorized("unauthorized"), false},
		{"bad request", apierrors.NewBadRequest("bad request"), false},
		{"timeout", apierrors.NewTimeoutError("timeout", 1), true},
		{"lost response", fmt.Errorf("connection lost"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fail := true
			sb := claimWriter(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if fail {
					return tc.err
				}
				return c.SubResource(name).Patch(ctx, obj, patch, opts...)
			}})
			a, allocatedA := deviceClaim(t, sb, "a", false)
			b, allocatedB := deviceClaim(t, sb, "b", false)
			if err := sb.patchClaimAllocation(t.Context(), a, allocatedA); err == nil {
				t.Fatal("expected PATCH failure")
			}
			fail = false
			err := sb.patchClaimAllocation(t.Context(), b, allocatedB)
			if (err != nil) != tc.uncertain {
				t.Fatalf("binding B: %v, want blocked=%t", err, tc.uncertain)
			}
			if tc.uncertain {
				if err := sb.patchClaimAllocation(t.Context(), a, allocatedA); err != nil {
					t.Fatal("same-claim retry failed", err)
				}
				releaseDeviceClaim(t, sb, a)
				if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestClaimDevicesAmbiguousWriteResolution(t *testing.T) {
	for _, resolution := range []string{"deleted", "replaced", "version advanced", "write persisted"} {
		t.Run(resolution, func(t *testing.T) {
			fail := true
			sb := claimWriter(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if !fail || resolution == "write persisted" {
					if err := c.SubResource(name).Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
				}
				if fail {
					return fmt.Errorf("response lost")
				}
				return nil
			}})
			a, allocatedA := deviceClaim(t, sb, "a", false)
			b, allocatedB := deviceClaim(t, sb, "b", false)
			if err := sb.patchClaimAllocation(t.Context(), a, allocatedA); err == nil {
				t.Fatal("expected ambiguous failure")
			}
			fail = false
			if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err == nil {
				t.Fatal("ambiguous write lost ownership")
			}
			switch resolution {
			case "deleted", "replaced":
				if err := sb.Delete(t.Context(), a); err != nil {
					t.Fatal(err)
				}
				if resolution == "replaced" {
					replacement := a.DeepCopy()
					replacement.UID, replacement.ResourceVersion = "new-uid", ""
					if err := sb.Create(t.Context(), replacement); err != nil {
						t.Fatal(err)
					}
				}
			case "version advanced":
				a.Annotations = map[string]string{"changed": "true"}
				if err := sb.Update(t.Context(), a); err != nil {
					t.Fatal(err)
				}
			case "write persisted":
				releaseDeviceClaim(t, sb, a)
			}
			if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err != nil {
				t.Fatal("resolved write retained ownership", err)
			}
			if len(sb.uncertainClaims) != 0 {
				t.Fatal("resolved write retained local state")
			}
		})
	}
}

func TestClaimDevicesReleaseAndReallocationWithoutWatchEvents(t *testing.T) {
	sb := claimWriter(interceptor.Funcs{})
	a, allocatedA := deviceClaim(t, sb, "a", false)
	b, allocatedB := deviceClaim(t, sb, "b", false)
	if err := sb.patchClaimAllocation(t.Context(), a, allocatedA); err != nil {
		t.Fatal(err)
	}
	releaseDeviceClaim(t, sb, a)
	allocatedA.ResourceVersion = a.ResourceVersion
	if err := sb.patchClaimAllocation(t.Context(), a, allocatedA); err != nil {
		t.Fatal(err)
	}
	// No watch events have been delivered, including the old allocation/release.
	if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err == nil {
		t.Fatal("B reused A's reallocated GPU")
	}
	releaseDeviceClaim(t, sb, a)
	if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err != nil {
		t.Fatal(err)
	}
}

func TestClaimBindingRejectsDeletedOrRecreatedClaim(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		t.Run(fmt.Sprint(recreate), func(t *testing.T) {
			sb := claimWriter(interceptor.Funcs{})
			a, allocatedA := deviceClaim(t, sb, "a", false)
			b, allocatedB := deviceClaim(t, sb, "b", false)
			if err := sb.Delete(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if recreate {
				replacement := a.DeepCopy()
				replacement.UID, replacement.ResourceVersion = "replacement", ""
				if err := sb.Create(t.Context(), replacement); err != nil {
					t.Fatal(err)
				}
			}
			err := sb.patchClaimAllocation(t.Context(), a, allocatedA)
			if recreate && !apierrors.IsConflict(err) || !recreate && !apierrors.IsNotFound(err) {
				t.Fatalf("unexpected stale binding result: %v", err)
			}
			if recreate {
				stored := &resourcev1.ResourceClaim{}
				if err := sb.Get(t.Context(), client.ObjectKeyFromObject(a), stored); err != nil {
					t.Fatal(err)
				}
				if stored.UID != "replacement" || stored.Status.Allocation != nil || len(stored.Status.ReservedFor) != 0 {
					t.Fatal("modified replacement claim")
				}
			}
			if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err != nil {
				t.Fatal("rejected write stranded device", err)
			}
		})
	}
}

func TestClaimDevicesLatePatchAfterTimeout(t *testing.T) {
	for _, fence := range []bool{false, true} {
		t.Run(fmt.Sprint(fence), func(t *testing.T) {
			var complete func() error
			sb := claimWriter(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if complete != nil {
					return c.SubResource(name).Patch(ctx, obj, patch, opts...)
				}
				saved := obj.(*resourcev1.ResourceClaim).DeepCopy()
				data, err := patch.Data(obj)
				if err != nil {
					return err
				}
				complete = func() error {
					return c.SubResource(name).Patch(ctx, saved, client.RawPatch(patch.Type(), data), opts...)
				}
				return apierrors.NewTimeoutError("request is still executing", 1)
			}})
			a, allocatedA := deviceClaim(t, sb, "a", false)
			b, allocatedB := deviceClaim(t, sb, "b", false)
			if err := sb.patchClaimAllocation(t.Context(), a, allocatedA); !apierrors.IsTimeout(err) {
				t.Fatal(err)
			}
			if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err == nil {
				t.Fatal("B allocated while A's PATCH could still commit")
			}
			if fence {
				a.Annotations = map[string]string{"canceled": "true"}
				if err := sb.Update(t.Context(), a); err != nil {
					t.Fatal(err)
				}
				if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err != nil {
					t.Fatal(err)
				}
				if err := complete(); !apierrors.IsConflict(err) {
					t.Fatalf("late PATCH was not fenced: %v", err)
				}
			} else {
				if err := complete(); err != nil {
					t.Fatal(err)
				}
				if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err == nil {
					t.Fatal("B reused A's late allocation")
				}
				releaseDeviceClaim(t, sb, a)
				if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
