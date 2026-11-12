// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
)

func TestClaimSuccessfulAllocationWithoutReadback(t *testing.T) {
	var calls []string
	sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, claim := obj.(*resourcev1.ResourceClaim); claim {
				return errors.New("unexpected claim GET")
			}
			return c.Get(ctx, key, obj, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			calls = append(calls, "create claim")
			return c.Create(ctx, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			switch obj.(type) {
			case *resourcev1.ResourceClaim:
				calls = append(calls, "allocate claim")
			case *corev1.Pod:
				calls = append(calls, "publish pod")
			}
			return c.SubResource(name).Patch(ctx, obj, patch, opts...)
		},
	})
	if err := sb.manageResourceClaim(context.Background(), pod, "node", resources); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"create claim", "allocate claim", "publish pod"}) {
		t.Fatalf("calls: %v", calls)
	}
	claims := &resourcev1.ResourceClaimList{}
	if err := sb.List(context.Background(), claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 1 || claims.Items[0].Status.Allocation == nil {
		t.Fatal("allocation was not persisted")
	}
}

func TestAllocatedClaimRecoveryAfterInventoryDisappears(t *testing.T) {
	sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{})
	ctx := context.Background()
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err != nil {
		t.Fatal(err)
	}
	claim := &resourcev1.ResourceClaim{}
	key := client.ObjectKey{Namespace: pod.Namespace, Name: allocationClaimName(pod)}
	if err := sb.Get(ctx, key, claim); err != nil {
		t.Fatal(err)
	}
	before := claim.DeepCopy()
	if err := sb.Delete(ctx, &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: "cpu-pool"}}); err != nil {
		t.Fatal(err)
	}
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err != nil {
		t.Fatalf("could not recover already allocated claim: %v", err)
	}
	if err := sb.Get(ctx, key, claim); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Status, claim.Status) {
		t.Fatal("rewrote existing allocation after inventory disappeared")
	}
	claim.OwnerReferences[0].UID = "another-pod"
	if err := sb.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err == nil {
		t.Fatal("recovered another Pod's claim")
	}
}

// cpuTopologyInformer supplies pre-synchronized inventory for allocation tests.
type cpuTopologyInformer struct {
	cache.ResourceEventHandlerRegistration
}

func (*cpuTopologyInformer) AddEventHandler(cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return &cpuTopologyInformer{}, nil
}
func (*cpuTopologyInformer) HasSynced() bool { return true }

func TestClaimUsesCachedCPUTopology(t *testing.T) {
	sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*resourcev1.ResourceSliceList); ok {
				return errors.New("unexpected ResourceSlice LIST")
			}
			return c.List(ctx, list, opts...)
		},
	})
	inventory := &resourcev1.ResourceSlice{}
	if err := sb.Get(context.Background(), client.ObjectKey{Name: "cpu-pool"}, inventory); err != nil {
		t.Fatal(err)
	}
	var err error
	sb.cpuTopology, err = nodeinfo.NewCPUTopologyCache(&cpuTopologyInformer{})
	if err != nil {
		t.Fatal(err)
	}
	sb.cpuTopology.OnAdd(inventory, true)
	if err := sb.manageResourceClaim(context.Background(), pod, "node", resources); err != nil {
		t.Fatal(err)
	}
}
