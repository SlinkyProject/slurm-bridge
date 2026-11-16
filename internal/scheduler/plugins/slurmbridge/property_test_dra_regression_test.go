// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"fmt"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRetainedClaimBindingAfterDeletion(t *testing.T) {
	t.Parallel()
	for _, recreate := range []bool{false, true} {
		t.Run(fmt.Sprintf("recreate=%t", recreate), func(t *testing.T) {
			t.Parallel()
			sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{})
			claim, _, allocation, err := sb.createRequestsAndMappings(t.Context(), pod, "node", resources)
			if err != nil {
				t.Fatal(err)
			}
			claim.UID = "deleted-claim"
			claim.Finalizers = nil // Release has removed the protection finalizer.
			if err := sb.Create(t.Context(), claim); err != nil {
				t.Fatal(err)
			}
			if err := sb.Delete(t.Context(), claim); err != nil {
				t.Fatal(err)
			}
			if recreate {
				replacement := claim.DeepCopy()
				replacement.UID, replacement.ResourceVersion = "replacement", ""
				if err := sb.Create(t.Context(), replacement); err != nil {
					t.Fatal(err)
				}
			}
			// The stale binder must neither reserve devices nor modify a replacement claim.
			err = sb.bindClaim(t.Context(), claim, pod, "node", allocation)
			if recreate {
				if !apierrors.IsConflict(err) {
					t.Fatalf("stale binding error=%v, want Conflict", err)
				}
				replacement := &resourcev1.ResourceClaim{}
				if err := sb.Get(t.Context(), client.ObjectKeyFromObject(claim), replacement); err != nil {
					t.Fatal(err)
				}
				if replacement.UID != "replacement" || replacement.Status.Allocation != nil {
					t.Fatal("stale binder modified replacement claim")
				}
			} else if !apierrors.IsNotFound(err) {
				t.Fatalf("stale binding error=%v, want NotFound", err)
			}
			other := pod.DeepCopy()
			other.Name, other.UID, other.ResourceVersion = "next-job", "next-pod", ""
			if err := sb.Create(t.Context(), other); err != nil {
				t.Fatal(err)
			}
			if err := sb.manageResourceClaim(t.Context(), other, "node", resources); err != nil {
				t.Fatalf("deleted claim stranded devices: %v", err)
			}
		})
	}
}
