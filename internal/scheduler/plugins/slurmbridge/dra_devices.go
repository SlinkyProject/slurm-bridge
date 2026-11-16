// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"maps"

	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// allocationClaimTracker is supplied by the scheduler's shared DRA manager.
type allocationClaimTracker interface {
	List() ([]*resourcev1.ResourceClaim, error)
	AssumeClaimAfterAPICall(*resourcev1.ResourceClaim) error
}

// allocationClaims falls back to live reads for callers without a scheduler.
func (sb *SlurmBridge) allocationClaims(ctx context.Context) ([]*resourcev1.ResourceClaim, error) {
	if sb.claimTracker != nil {
		return sb.claimTracker.List()
	}
	list := &resourcev1.ResourceClaimList{}
	if err := sb.List(ctx, list); err != nil {
		return nil, err
	}
	claims := make([]*resourcev1.ResourceClaim, len(list.Items))
	for i := range list.Items {
		claims[i] = &list.Items[i]
	}
	return claims, nil
}

// rememberClaim publishes successful writes to the shared assume cache. A new
// claim may not be in the informer yet; retain ownership locally until it is.
func (sb *SlurmBridge) rememberClaim(claim *resourcev1.ResourceClaim) {
	if sb.claimTracker == nil {
		return
	}
	if err := sb.claimTracker.AssumeClaimAfterAPICall(claim); err == nil {
		return
	}
	if sb.uncertainClaims == nil {
		sb.uncertainClaims = make(map[string]*resourcev1.ResourceClaim)
	}
	sb.uncertainClaims[claimOwner(claim)] = claim.DeepCopy()
}

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

// patchClaimAllocation serializes this plugin's ownership checks and writes.
// Normal binds use the shared assume cache. Only ambiguous writes and successful
// writes not yet accepted by that cache require individual live reads.
func (sb *SlurmBridge) patchClaimAllocation(ctx context.Context, claim, toUpdate *resourcev1.ResourceClaim) error {
	sb.claimMu.Lock()
	defer sb.claimMu.Unlock()
	claims, err := sb.allocationClaims(ctx)
	if err != nil {
		return err
	}
	for owner, uncertain := range maps.Clone(sb.uncertainClaims) {
		current := &resourcev1.ResourceClaim{}
		err := sb.Get(ctx, client.ObjectKeyFromObject(uncertain), current)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if apierrors.IsNotFound(err) || claimOwner(current) != owner {
			delete(sb.uncertainClaims, owner)
			// A replacement may itself own devices before its informer event arrives.
			if err == nil {
				claims = append(claims, current)
				if current.Status.Allocation != nil {
					sb.rememberClaim(current)
				}
			}
			continue
		}
		if current.ResourceVersion != uncertain.ResourceVersion || current.Status.Allocation != nil {
			// A changed version fences the old conditional PATCH, but an allocation
			// discovered live still owns its devices even if the tracker is behind.
			delete(sb.uncertainClaims, owner)
			if current.Status.Allocation != nil {
				sb.rememberClaim(current)
			}
		}
		claims = append(claims, current)
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
	for _, other := range claims {
		if claimOwner(other) != owner {
			if err := checkClaimDevices(devices, other); err != nil {
				return err
			}
		}
	}
	err = sb.Status().Patch(ctx, toUpdate, client.StrategicMergeFrom(claim, client.MergeFromWithOptimisticLock{}))
	if err == nil {
		delete(sb.uncertainClaims, owner)
		sb.rememberClaim(toUpdate)
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
