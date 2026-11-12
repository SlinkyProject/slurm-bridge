// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInventoryCacheKeepsClaimReadbackAndWritesLive(t *testing.T) {
	ctx := context.Background()
	staleClaim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "ns", Labels: map[string]string{"version": "stale"}}}
	liveClaim := staleClaim.DeepCopy()
	liveClaim.Labels["version"] = "live"
	class := &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "gpu"}}
	slice := &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: "node-gpu"}}
	inventory := fake.NewClientBuilder().WithObjects(class, slice, staleClaim).Build()
	direct := fake.NewClientBuilder().WithObjects(liveClaim).Build()
	c := WithCachedInventory(WithInventoryReader(ctx, inventory), direct)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "gpu"}, &resourcev1.DeviceClass{}))
	slices := &resourcev1.ResourceSliceList{}
	require.NoError(t, c.List(ctx, slices))
	require.Len(t, slices.Items, 1)
	claim := &resourcev1.ResourceClaim{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(liveClaim), claim))
	require.Equal(t, "live", claim.Labels["version"])
	claims := &resourcev1.ResourceClaimList{}
	require.NoError(t, c.List(ctx, claims))
	require.Equal(t, "live", claims.Items[0].Labels["version"])
	created := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "ns"}}
	require.NoError(t, c.Create(ctx, created))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(created), &resourcev1.ResourceClaim{}))
	require.True(t, apierrors.IsNotFound(inventory.Get(ctx, client.ObjectKeyFromObject(created), &resourcev1.ResourceClaim{})))
	require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Name: "pod", Namespace: "ns"}, &corev1.Pod{})))
	// Watch updates/deletions are observed through the supplied cache, with no
	// second memoization layer retaining stale inventory in the wrapper.
	require.NoError(t, inventory.Delete(ctx, class))
	require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Name: "gpu"}, &resourcev1.DeviceClass{})))
	require.Same(t, direct, WithCachedInventory(ctx, direct))
}
