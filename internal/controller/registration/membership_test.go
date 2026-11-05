// SPDX-FileCopyrightText: Copyright (C) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIndexedMembershipClientListsGroupMembers(t *testing.T) {
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
	c := indexedMembershipClient{Client: kube}

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

}
