// SPDX-FileCopyrightText: Copyright (C) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// recordingReader stands in for the manager cache and records what reached it.
type recordingReader struct{ kinds []string }

func (r *recordingReader) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	r.kinds = append(r.kinds, obj.GetObjectKind().GroupVersionKind().Kind)
	return nil
}

func (r *recordingReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	r.kinds = append(r.kinds, list.GetObjectKind().GroupVersionKind().Kind)
	return nil
}

func TestIndexedMembershipClientReadsFromCache(t *testing.T) {
	member := func(name, group string) *corev1.Pod {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name}}
		if group != "" {
			pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(group)}
		}
		return pod
	}
	kube := fake.NewClientBuilder().
		WithObjects(member("a", "pg"), member("b", "pg"), member("c", "other"), member("d", "")).
		WithIndex(&corev1.Pod{}, podGroupMemberIndex, nativePodGroupKeys).
		WithIndex(&corev1.Pod{}, publishedJobIndex, publishedJobKeys).Build()
	cache := &recordingReader{}
	c := indexedMembershipClient{Client: kube, cache: cache}

	pods, err := c.ListPodGroupMembers(context.Background(), "ns", "pg")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, pod := range pods.Items {
		names = append(names, pod.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"a", "b"}) {
		t.Errorf("members = %v, want [a b]", names)
	}

	composites := &unstructured.UnstructuredList{}
	composites.SetAPIVersion("scheduling.k8s.io/v1alpha3")
	composites.SetKind("CompositePodGroupList")
	if err := c.List(context.Background(), composites, client.InNamespace("ns")); err != nil {
		t.Fatal(err)
	}
	composite := &unstructured.Unstructured{}
	composite.SetAPIVersion("scheduling.k8s.io/v1alpha3")
	composite.SetKind("CompositePodGroup")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "root"}, composite); err != nil {
		t.Fatal(err)
	}
	if err := c.List(context.Background(), &corev1.PodList{}, client.InNamespace("ns")); err != nil {
		t.Fatal(err)
	}
	if want := []string{"CompositePodGroupList", "CompositePodGroup"}; !slices.Equal(cache.kinds, want) {
		t.Errorf("cache reads = %v, want %v", cache.kinds, want)
	}
}
