// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"maps"
	"slices"

	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type claimDevice struct{ driver, pool, device string }

func claimOwner(claim *resourcev1.ResourceClaim) string {
	return claim.Namespace + "/" + claim.Name + "/" + string(claim.UID)
}

func claimedDevices(claim *resourcev1.ResourceClaim) map[claimDevice]bool {
	devices := make(map[claimDevice]bool)
	if claim.Status.Allocation == nil {
		return devices
	}
	for _, result := range claim.Status.Allocation.Devices.Results {
		if ptr.Deref(result.AdminAccess, false) {
			continue
		}
		id := claimDevice{result.Driver, result.Pool, result.Device}
		shared := result.ShareID != nil
		if previous, found := devices[id]; found {
			shared = shared && previous
		}
		devices[id] = shared
	}
	return devices
}

func checkClaimDevices(devices map[claimDevice]bool, other *resourcev1.ResourceClaim) error {
	for id, otherShared := range claimedDevices(other) {
		if shared, found := devices[id]; found && (!shared || !otherShared) {
			return fmt.Errorf("DRA device %s/%s/%s is still allocated to claim %s", id.driver, id.pool, id.device, claimOwner(other))
		}
	}
	return nil
}

// patchClaimAllocation serializes the live ownership check and conditional write.
// ResourceClaim reads must use the direct API client, as they do in New.
// Only ambiguous failures survive this call; successful writes need no local cache.
func (sb *SlurmBridge) patchClaimAllocation(ctx context.Context, claim, toUpdate *resourcev1.ResourceClaim) error {
	sb.claimMu.Lock()
	defer sb.claimMu.Unlock()
	claims := &resourcev1.ResourceClaimList{}
	if err := sb.List(ctx, claims); err != nil {
		return err
	}
	for owner, uncertain := range sb.uncertainClaims {
		// An absent/replaced claim or changed version fences the old conditional
		// PATCH: it either already committed or can no longer commit.
		if !slices.ContainsFunc(claims.Items, func(current resourcev1.ResourceClaim) bool {
			return claimOwner(&current) == owner && current.ResourceVersion == uncertain.ResourceVersion
		}) {
			delete(sb.uncertainClaims, owner)
		}
	}
	owner, devices := claimOwner(claim), claimedDevices(toUpdate)
	for other, uncertain := range sb.uncertainClaims {
		if other == owner {
			if !maps.Equal(devices, claimedDevices(uncertain)) {
				return fmt.Errorf("claim %s has an unresolved allocation for different devices", owner)
			}
		} else if err := checkClaimDevices(devices, uncertain); err != nil {
			return err
		}
	}
	for i := range claims.Items {
		other := &claims.Items[i]
		if claimOwner(other) != owner {
			if err := checkClaimDevices(devices, other); err != nil {
				return err
			}
		}
	}
	err := sb.Status().Patch(ctx, toUpdate, client.StrategicMergeFrom(claim, client.MergeFromWithOptimisticLock{}))
	if err == nil {
		delete(sb.uncertainClaims, owner)
	} else if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) && !apierrors.IsInvalid(err) &&
		!apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err) && !apierrors.IsBadRequest(err) {
		if sb.uncertainClaims == nil {
			sb.uncertainClaims = make(map[string]*resourcev1.ResourceClaim)
		}
		if sb.uncertainClaims[owner] == nil {
			uncertain := toUpdate.DeepCopy()
			uncertain.ResourceVersion = claim.ResourceVersion
			sb.uncertainClaims[owner] = uncertain
		}
	}
	return err
}
