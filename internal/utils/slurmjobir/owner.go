// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const maxControllerOwnerDepth = 32

func getRootOwnerMetadata(c client.Client, ctx context.Context, obj client.Object) (*metav1.PartialObjectMetadata, error) {
	return resolveRootOwnerMetadata(c, ctx, obj, false, 0, nil)
}

func resolveRootOwnerMetadata(
	c client.Client,
	ctx context.Context,
	obj client.Object,
	controllerResolved bool,
	depth int,
	supportedRoot *metav1.PartialObjectMetadata,
) (*metav1.PartialObjectMetadata, error) {
	namespace := obj.GetNamespace()
	objGVK, err := apiutil.GVKForObject(obj, c.Scheme())
	if err != nil {
		return nil, err
	}
	metadata := obj.(metav1.ObjectMetaAccessor).GetObjectMeta()
	currentPOM := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			Kind:       objGVK.Kind,
			APIVersion: objGVK.GroupVersion().String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metadata.GetNamespace(),
			Name:      metadata.GetName(),
		},
	}
	if controllerResolved && isSupportedWorkload(objGVK) {
		supportedRoot = currentPOM
	}

	owner := getNextControllerOwner(obj)
	if owner == nil {
		if supportedRoot != nil {
			return supportedRoot, nil
		}
		return currentPOM, nil
	}
	if depth >= maxControllerOwnerDepth {
		return nil, fmt.Errorf("controller owner chain exceeds maximum depth of %d at %s %s/%s", maxControllerOwnerDepth, objGVK.String(), namespace, obj.GetName())
	}
	ownerPOM := &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      owner.Name,
		},
	}
	ownerGVK := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind)
	ownerPOM.SetGroupVersionKind(ownerGVK)

	key := client.ObjectKey{Namespace: namespace, Name: owner.Name}
	if err := c.Get(ctx, key, ownerPOM); err != nil {
		// Fall back only when RBAC forbids access to an unsupported higher
		// controller. Prefer the highest supported workload already resolved;
		// otherwise use the highest readable controller. Missing owners and
		// supported workloads indicate broken owner chains or RBAC and must
		// remain scheduling errors. The Pod itself is not a controller fallback,
		// so its direct owner must be readable.
		if controllerResolved && apierrors.IsForbidden(err) && !isSupportedWorkload(ownerGVK) {
			if supportedRoot != nil {
				return supportedRoot, nil
			}
			return currentPOM, nil
		}
		return nil, err
	}

	return resolveRootOwnerMetadata(c, ctx, ownerPOM, true, depth+1, supportedRoot)
}

func getNextControllerOwner(obj client.Object) *metav1.OwnerReference {
	owners := obj.GetOwnerReferences()
	for _, owner := range owners {
		if ptr.Deref(owner.Controller, false) {
			return &owner
		}
	}
	return nil
}
