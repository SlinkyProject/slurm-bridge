// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

func GetRootOwnerMetadata(c client.Client, ctx context.Context, obj client.Object) (*metav1.PartialObjectMetadata, error) {
	namespace := obj.GetNamespace()
	objGVK, err := apiutil.GVKForObject(obj, c.Scheme())
	if err != nil {
		return nil, err
	}
	metadata := obj.(metav1.ObjectMetaAccessor).GetObjectMeta()
	pom := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			Kind:       objGVK.Kind,
			APIVersion: objGVK.GroupVersion().String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metadata.GetNamespace(),
			Name:      metadata.GetName(),
		},
	}

	owner := getNextControllerOwner(obj)
	if owner == nil {
		// Found root owner
		return pom, nil
	}
	name := owner.Name
	pom.Name = name
	ownerGVK := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind)
	pom.SetGroupVersionKind(ownerGVK)

	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := c.Get(ctx, key, pom); err != nil {
		return nil, err
	}

	// Follow owner reference
	return GetRootOwnerMetadata(c, ctx, pom)
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

func NamespacedNameFromString(qualifiedName string) types.NamespacedName {
	parts := strings.Split(qualifiedName, "/")
	if len(parts) != 2 {
		return types.NamespacedName{
			Name: parts[0],
		}
	}
	return types.NamespacedName{
		Namespace: parts[0],
		Name:      parts[1],
	}
}
