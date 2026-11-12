// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/controllercheck"
)

// The authority is the fake API's persisted claims, not scheduler-local state.
// PATCH may fail before commit, lose its response after commit, complete after
// a timeout, or race deletion. Retained binding snapshots and in-flight writes
// can outlive deletion/recreation or a resource-version change.
//
// Safety is checked at every committed allocation. Recovery stops faults and
// stale writers, deletes remaining claims, and completes in-flight writes:
// it must then be possible to reuse the devices without restarting the scheduler.
// We never synthesize an extra deletion to clean up a leaked reservation.
func TestSchedulerDRAClaimLifecycleProperties(t *testing.T) {
	t.Parallel()
	controllercheck.ExploreParallel(t, draPropertyContract, func(rt *rapid.T) controllercheck.Campaign[draPropertyState] {
		w := newDRAPropertyWorld(rt)
		for range rapid.IntRange(2, 4).Draw(rt, "initial-claims") {
			w.create(rt, nil)
		}
		record := func(rt *rapid.T) *draPropertyRecord {
			return w.records[rapid.IntRange(0, len(w.records)-1).Draw(rt, "claim")]
		}
		return controllercheck.Campaign[draPropertyState]{
			Trace:          &w.trace,
			Snapshot:       func(context.Context) (draPropertyState, error) { return w.snapshot(rt), nil },
			ObserveEffects: func(observe func()) { w.observe = observe },
			Actions: map[string]func(*rapid.T) error{
				"restart": func(*rapid.T) error {
					// TODO: Restore restart with outstanding PATCHes after reproducing
					// late commits against a real API server, including cancellation,
					// request deadlines, and leader-election handoff timing.
					if len(w.pending) > 0 {
						return nil
					}
					w.sb = &SlurmBridge{Client: w.sb.Client, draRegistry: w.sb.draRegistry}
					return nil
				},
				"bind": func(rt *rapid.T) error {
					w.setOutcome(rapid.SampledFrom([]string{"success", "success", "before-commit", "after-commit", "delayed-commit", "list-failure", "delete-before-response"}).Draw(rt, "patch-outcome"))
					// A denied allocation is expected; committed effects are checked by the contract.
					_ = w.bind(rt, record(rt))
					return nil
				},
				"complete": func(rt *rapid.T) error {
					if len(w.pending) > 0 {
						w.complete(rt, rapid.IntRange(0, len(w.pending)-1).Draw(rt, "pending-write"))
					}
					return nil
				},
				"version": func(rt *rapid.T) error {
					if claim := w.current(rt, record(rt)); claim != nil {
						claim.Annotations = map[string]string{"revision": claim.ResourceVersion}
						return w.sb.Update(rt.Context(), claim)
					}
					return nil
				},
				"delete":  func(rt *rapid.T) error { w.delete(rt, record(rt)); return nil },
				"release": func(rt *rapid.T) error { w.release(rt, record(rt)); return nil },
				"recreate": func(rt *rapid.T) error {
					r := record(rt)
					w.delete(rt, r)
					if w.current(rt, r) == nil {
						w.create(rt, r)
					}
					return nil
				},
			},
			Recover: func(context.Context) error {
				w.setOutcome("success")
				for _, r := range w.records {
					w.delete(rt, r)
				}
				return nil
			},
			Progress: func(context.Context) error {
				if w.probe != nil && w.probeErr == nil {
					return nil
				} // Already allocated; no new bind is needed.
				for len(w.pending) > 0 {
					w.complete(rt, 0)
				}
				// Fairly retry uncertain writes against the settled API, then probe reuse.
				for _, r := range w.records {
					if w.uncertain[r] {
						_ = w.bind(rt, r)
					}
				}
				// Preserve the original recovery order: settle EVERY uncertain
				// writer before a new owner can acquire and block its retries.
				for _, pending := range w.uncertain {
					if pending {
						return nil
					}
				}
				if w.probe == nil {
					w.probe = w.create(rt, nil)
				}
				w.probeErr = w.bind(rt, w.probe)
				return nil
			},
			Restart: func(context.Context) error {
				// This implementation reads ownership directly from the API. Recovery
				// has already checked reuse before a fresh instance drops local state.
				w.sb = &SlurmBridge{Client: w.sb.Client, draRegistry: w.sb.draRegistry}
				return nil
			},
			RecoverySteps: 8, // Each tick retries all unresolved writes, not one key.
		}
	})
}

type draPropertyState struct {
	claims      []resourcev1.ResourceClaim
	identityErr error
	readErr     error
	unresolved  bool
	probed      bool
	probeErr    error
}

var draPropertyContract = controllercheck.Contract[draPropertyState]{
	Always: []controllercheck.Rule[draPropertyState]{
		{Name: "ownership-read", Statement: "failed ownership reads never permit a PATCH or successful binding", Check: func(s draPropertyState) error { return s.readErr }},
		{Name: "exclusive-ownership", Statement: "persisted claims never have incompatible device allocations", Check: checkDRAOwnership},
		{Name: "claim-identity", Statement: "a stale binding never modifies a replacement UID", Check: func(s draPropertyState) error { return s.identityErr }},
	},
	Eventually: []controllercheck.Rule[draPropertyState]{
		{Name: "device-reuse", Statement: "after deletion, in-flight writes and retries settle, a new claim can acquire the devices", Check: func(s draPropertyState) error {
			if s.unresolved {
				return fmt.Errorf("ambiguous binding has not settled")
			}
			if !s.probed {
				return fmt.Errorf("reuse has not been attempted")
			}
			return s.probeErr
		}},
	},
}

func (w *draPropertyWorld) snapshot(rt *rapid.T) draPropertyState {
	claims := &resourcev1.ResourceClaimList{}
	if err := w.sb.List(rt.Context(), claims); err != nil {
		rt.Fatal(err)
	}
	s := draPropertyState{claims: claims.Items, identityErr: w.identityErr, readErr: w.readErr, unresolved: len(w.pending) > 0, probed: w.probe != nil, probeErr: w.probeErr}
	for _, pending := range w.uncertain {
		s.unresolved = s.unresolved || pending
	}
	return s
}

func (w *draPropertyWorld) setOutcome(outcome string) {
	w.outcome = outcome
	w.faults.Heal()
	if outcome == "before-commit" || outcome == "after-commit" {
		w.faults.Arm("claim.patch", controllercheck.FailureMode(outcome))
	}
}

// Adapt fixed fixture setup to Rapid so setup failures shrink with their history.
type rapidFixtureT struct{ *rapid.T }

func (rapidFixtureT) Helper() {}

type draPropertyRecord struct {
	pod        *corev1.Pod
	claim      *resourcev1.ResourceClaim
	allocation *claimAllocation
}

type draPropertyWorld struct {
	trace       controllercheck.Trace
	faults      controllercheck.Faults
	observe     func()
	identityErr error
	readErr     error
	probe       *draPropertyRecord
	probeErr    error
	sb          *SlurmBridge
	base        *corev1.Pod
	template    *resourcev1.ResourceClaim
	allocation  *claimAllocation
	records     []*draPropertyRecord
	pending     []func() error
	failList    bool
	readFailed  bool
	outcome     string
	binding     *draPropertyRecord
	uncertain   map[*draPropertyRecord]bool
}

func newDRAPropertyWorld(rt *rapid.T) *draPropertyWorld {
	w := &draPropertyWorld{outcome: "success", uncertain: make(map[*draPropertyRecord]bool)}
	w.faults.Trace = &w.trace
	// Fixed CPU/GPU inventory: CPU socket allocations are shareable; the GPU
	// is exclusive. Every generated claimant contends for that same inventory.
	sb, pod, resources := socketClaimFixture(rapidFixtureT{rt}, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*resourcev1.ResourceClaimList); ok && w.failList {
				w.failList, w.readFailed = false, true
				return controllercheck.ErrInjected
			}
			return c.List(ctx, list, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			claim, ok := obj.(*resourcev1.ResourceClaim)
			if !ok || sub != "status" {
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			}
			if w.readFailed {
				w.readErr = fmt.Errorf("PATCH attempted after failed ownership read")
			}
			commit := func() error {
				uid := claim.UID
				if err := c.SubResource(sub).Patch(ctx, obj, patch, opts...); err != nil {
					return err
				}
				if claim.UID != uid {
					w.identityErr = fmt.Errorf("stale binding for UID %s modified replacement UID %s", uid, claim.UID)
				}
				if w.observe != nil {
					w.observe()
				} // Capture safety before any later release/deletion.
				return nil
			}
			var err error
			if w.outcome == "delayed-commit" {
				data, marshalErr := patch.Data(claim)
				if marshalErr != nil {
					rt.Fatal(marshalErr)
				}
				claim = claim.DeepCopy()
				obj, patch = claim, client.RawPatch(patch.Type(), data)
				w.pending = append(w.pending, commit)
				err = controllercheck.ErrInjected
			} else {
				err = w.faults.Call("claim.patch", commit)
				if err == nil && w.outcome == "delete-before-response" {
					w.deleteClaim(rt, claim)
				}
			}
			w.uncertain[w.binding] = errors.Is(err, controllercheck.ErrInjected)
			return err
		},
	})
	w.sb, w.base = sb, pod
	var err error
	w.template, _, w.allocation, err = sb.createRequestsAndMappings(rt.Context(), pod, "node", resources)
	if err != nil {
		rt.Fatal(err)
	}
	return w
}

func (w *draPropertyWorld) create(rt *rapid.T, previous *draPropertyRecord) *draPropertyRecord {
	number := len(w.records)
	claim := w.template.DeepCopy()
	pod := w.base.DeepCopy()
	pod.Name, pod.UID = fmt.Sprintf("pod-%d", number), types.UID(fmt.Sprintf("pod-uid-%d", number))
	claim.Name = fmt.Sprintf("claim-%d", number)
	if previous != nil {
		claim.Name, claim.Namespace = previous.claim.Name, previous.claim.Namespace
		pod = previous.pod.DeepCopy()
	}
	// Claims are recreated only after the old object is actually gone.
	existing := &resourcev1.ResourceClaim{}
	err := w.sb.Get(rt.Context(), client.ObjectKeyFromObject(claim), existing)
	if err == nil {
		return previous
	}
	if !apierrors.IsNotFound(err) {
		rt.Fatal(err)
	}
	claim.UID = types.UID(fmt.Sprintf("claim-uid-%d", number))
	claim.ResourceVersion = ""
	claim.Finalizers = nil // The model includes completed protection-finalizer removal.
	claim.Status = resourcev1.ResourceClaimStatus{}
	if err := w.sb.Create(rt.Context(), claim); err != nil {
		rt.Fatal(err)
	}
	r := &draPropertyRecord{pod: pod, claim: claim.DeepCopy(), allocation: w.allocation}
	w.records = append(w.records, r)
	return r
}

func (w *draPropertyWorld) current(rt *rapid.T, r *draPropertyRecord) *resourcev1.ResourceClaim {
	claim := &resourcev1.ResourceClaim{}
	err := w.sb.Get(rt.Context(), client.ObjectKeyFromObject(r.claim), claim)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		rt.Fatal(err)
	}
	if claim.UID != r.claim.UID {
		return nil
	}
	return claim
}

func (w *draPropertyWorld) bind(rt *rapid.T, r *draPropertyRecord) error {
	w.binding = r
	claim := w.current(rt, r)
	if claim == nil {
		claim = r.claim.DeepCopy()
	}
	w.failList, w.readFailed = w.outcome == "list-failure", false
	err := w.sb.bindClaim(rt.Context(), claim, r.pod, "node", r.allocation)
	if w.readFailed && err == nil {
		w.readErr = fmt.Errorf("binding succeeded after failed ownership read")
	}
	w.failList, w.readFailed = false, false
	return err
}

func (w *draPropertyWorld) delete(rt *rapid.T, r *draPropertyRecord) {
	if claim := w.current(rt, r); claim != nil {
		w.deleteClaim(rt, claim)
	}
}

func (w *draPropertyWorld) deleteClaim(rt *rapid.T, claim *resourcev1.ResourceClaim) {
	if err := w.sb.Delete(rt.Context(), claim); err != nil {
		rt.Fatal(err)
	}
}

func (w *draPropertyWorld) release(rt *rapid.T, r *draPropertyRecord) {
	claim := w.current(rt, r)
	if claim == nil || claim.Status.Allocation == nil {
		return
	}
	claim.Status = resourcev1.ResourceClaimStatus{}
	if err := w.sb.Status().Update(rt.Context(), claim); err != nil {
		rt.Fatal(err)
	}
}

func (w *draPropertyWorld) complete(rt *rapid.T, index int) {
	commit := w.pending[index]
	w.pending = append(w.pending[:index], w.pending[index+1:]...)
	err := commit()
	w.trace.Logf("late PATCH: %v", err)
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		rt.Fatal(err)
	}
}

func checkDRAOwnership(s draPropertyState) error {
	// Pairwise compare raw API allocations. Do not reuse claimedDevices,
	// claimOwner, or any pending/index bookkeeping from the implementation.
	for i, a := range s.claims {
		if a.Status.Allocation == nil {
			continue
		}
		for _, b := range s.claims[i+1:] {
			if b.Status.Allocation == nil {
				continue
			}
			for _, x := range a.Status.Allocation.Devices.Results {
				for _, y := range b.Status.Allocation.Devices.Results {
					admin := x.AdminAccess != nil && *x.AdminAccess || y.AdminAccess != nil && *y.AdminAccess
					if !admin && x.Driver == y.Driver && x.Pool == y.Pool && x.Device == y.Device && (x.ShareID == nil || y.ShareID == nil) {
						return fmt.Errorf("incompatible persisted owners %s/%s and %s/%s for %s/%s/%s", a.Name, a.UID, b.Name, b.UID, x.Driver, x.Pool, x.Device)
					}
				}
			}
		}
	}
	return nil
}

// Replay the ownership failure without recreation or unrelated actions.
func TestRetainedDRAReleaseAndReallocation(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		w := newDRAPropertyWorld(rt)
		a, b := w.create(rt, nil), w.create(rt, nil)
		if err := w.bind(rt, a); err != nil {
			rt.Fatal(err)
		}
		w.release(rt, a)
		if err := w.bind(rt, a); err != nil {
			rt.Fatal(err)
		}
		// The API still allocates the GPU to A after release and reallocation.
		err := w.bind(rt, b)
		if violation := checkDRAOwnership(w.snapshot(rt)); violation != nil {
			rt.Fatal(violation)
		}
		if err == nil {
			rt.Fatal("admitted B while A still owns the device")
		}
	})
}
