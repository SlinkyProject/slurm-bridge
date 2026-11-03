// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Allowed paths from the Pod's owner outward, including ordinal and hash-based
// LeaderWorkerSet intermediates. Other controller references are ignored.
var workloadOwnerPaths = [][]metav1.TypeMeta{
	{job_v1, jobSet_v1alpha2},
	{lws_v1},
	{{APIVersion: "apps/v1", Kind: "StatefulSet"}, lws_v1},
	{pod_v1, lws_v1},
	{{APIVersion: "apps/v1", Kind: "StatefulSet"}, pod_v1, lws_v1},
	{{APIVersion: "apps/v1", Kind: "StatefulSet"}, pod_v1, {APIVersion: "apps/v1", Kind: "StatefulSet"}, lws_v1},
}

func getRootOwnerMetadata(c client.Reader, ctx context.Context, pod *corev1.Pod) (*metav1.PartialObjectMetadata, error) {
	root := &metav1.PartialObjectMetadata{TypeMeta: pod_v1, ObjectMeta: *pod.ObjectMeta.DeepCopy()}
	var obj client.Object = pod
	paths := workloadOwnerPaths
	for depth := 0; len(paths) > 0; depth++ {
		owner := getNextControllerOwner(obj)
		if owner == nil {
			break
		}
		kind := metav1.TypeMeta{APIVersion: owner.APIVersion, Kind: owner.Kind}
		var matching [][]metav1.TypeMeta
		for _, path := range paths {
			if depth < len(path) && path[depth] == kind {
				matching = append(matching, path)
			}
		}
		if len(matching) == 0 {
			break
		}
		metadata := &metav1.PartialObjectMetadata{TypeMeta: kind}
		key := client.ObjectKey{Namespace: obj.GetNamespace(), Name: owner.Name}
		if err := c.Get(ctx, key, metadata); err != nil {
			return nil, err
		}
		switch kind {
		case job_v1, jobSet_v1alpha2, lws_v1:
			root = metadata
		}
		obj, paths = metadata, matching
	}
	return root, nil
}

func getNextControllerOwner(obj client.Object) *metav1.OwnerReference {
	for _, owner := range obj.GetOwnerReferences() {
		if ptr.Deref(owner.Controller, false) {
			return &owner
		}
	}
	return nil
}
