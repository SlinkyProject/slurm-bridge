// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/client-go/informers"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	"k8s.io/kubernetes/pkg/scheduler/util/assumecache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestClaimTrackerOwnership(t *testing.T) {
	for _, mode := range []string{"observed", "new claim", "lost response", "late patch"} {
		t.Run(mode, func(t *testing.T) {
			gets := 0
			var late func() error
			first := true
			sb := claimWriter(interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					t.Fatal("binding issued a live cluster-wide LIST")
					return nil
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					gets++
					return c.Get(ctx, key, obj, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if first && mode == "late patch" {
						first = false
						data, err := patch.Data(obj)
						if err != nil {
							return err
						}
						saved := obj.DeepCopyObject().(client.Object)
						late = func() error {
							return c.SubResource(sub).Patch(ctx, saved, client.RawPatch(patch.Type(), data), opts...)
						}
						return fmt.Errorf("timeout before commit")
					}
					if err := c.SubResource(sub).Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
					if first && mode == "lost response" {
						first = false
						return fmt.Errorf("response lost after commit")
					}
					first = false
					return nil
				},
			})
			factory := informers.NewSharedInformerFactory(kubefake.NewClientset(), 0)
			cache := assumecache.NewAssumeCache(klog.Background(), factory.Resource().V1().ResourceClaims().Informer(), "claims", "", nil)
			tracker := dynamicresources.NewDRAManager(t.Context(), cache, nil, factory).ResourceClaims()
			sb.claimTracker = tracker
			a, allocatedA := deviceClaim(t, sb, "a", false)
			b, allocatedB := deviceClaim(t, sb, "b", false)
			if mode == "observed" {
				assumecache.AddTestObject(cache, a.DeepCopy())
			}
			err := sb.patchClaimAllocation(t.Context(), a, allocatedA)
			wantError := mode == "lost response" || mode == "late patch"
			if (err != nil) != wantError {
				t.Fatalf("first PATCH: %v", err)
			}
			if gets != 0 {
				t.Fatal("successful path performed a live GET")
			}
			if mode == "observed" {
				seen, err := tracker.Get(a.Namespace, a.Name)
				if err != nil || seen.Status.Allocation == nil {
					t.Fatalf("shared tracker missed write: %v", err)
				}
			}
			for attempt := range 2 {
				if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err == nil {
					t.Fatal("reused device before informer saw allocation")
				}
				if attempt == 0 && late != nil {
					if err := late(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "observed" && gets != 0 {
				t.Fatal("observed claims needed live GETs")
			}
			// Deliver the informer add after several competing attempts. Ownership
			// must move into the shared tracker without leaving a local reservation.
			current := &resourcev1.ResourceClaim{}
			if err := sb.Get(t.Context(), client.ObjectKeyFromObject(a), current); err != nil {
				t.Fatal(err)
			}
			assumecache.AddTestObject(cache, current.DeepCopy())
			if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err == nil {
				t.Fatal("lost ownership at tracker handoff")
			}
			if len(sb.uncertainClaims) != 0 {
				t.Fatal("retained local ownership after tracker caught up")
			}
			releaseDeviceClaim(t, sb, a)
			assumecache.AddTestObject(cache, a.DeepCopy())
			if err := sb.patchClaimAllocation(t.Context(), b, allocatedB); err != nil {
				t.Fatalf("release stranded device: %v", err)
			}
		})
	}
}

func TestClaimTrackerFencesAmbiguousWrites(t *testing.T) {
	for _, resolution := range []string{"deleted", "version advanced", "allocated replacement", "read failure"} {
		t.Run(resolution, func(t *testing.T) {
			failRead := false
			sb := claimWriter(interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					t.Fatal("unexpected live LIST")
					return nil
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if failRead {
						return fmt.Errorf("live read failed")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			factory := informers.NewSharedInformerFactory(kubefake.NewClientset(), 0)
			cache := assumecache.NewAssumeCache(klog.Background(), factory.Resource().V1().ResourceClaims().Informer(), "claims", "", nil)
			sb.claimTracker = dynamicresources.NewDRAManager(t.Context(), cache, nil, factory).ResourceClaims()
			a, allocatedA := deviceClaim(t, sb, "a", false)
			b, allocatedB := deviceClaim(t, sb, "b", false)
			// An earlier PATCH timed out with no observable commit.
			sb.uncertainClaims = map[string]*resourcev1.ResourceClaim{claimOwner(a): allocatedA.DeepCopy()}
			switch resolution {
			case "deleted", "allocated replacement":
				if err := sb.Delete(t.Context(), a); err != nil {
					t.Fatal(err)
				}
				if resolution == "allocated replacement" {
					replacement := a.DeepCopy()
					replacement.UID, replacement.ResourceVersion = "replacement", ""
					if err := sb.Create(t.Context(), replacement); err != nil {
						t.Fatal(err)
					}
					replacement.Status = *allocatedA.Status.DeepCopy()
					if err := sb.Status().Update(t.Context(), replacement); err != nil {
						t.Fatal(err)
					}
				}
			case "version advanced":
				a.Annotations = map[string]string{"fence": "true"}
				if err := sb.Update(t.Context(), a); err != nil {
					t.Fatal(err)
				}
			case "read failure":
				failRead = true
			}
			for range 2 {
				err := sb.patchClaimAllocation(t.Context(), b, allocatedB)
				blocked := resolution == "allocated replacement" || resolution == "read failure"
				if (err != nil) != blocked {
					t.Fatalf("bind error=%v, want blocked=%v", err, blocked)
				}
				if !blocked {
					break
				}
			}
		})
	}
}
