// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
)

// Translation discovers the indexed member lookup through this interface.
var _ interface {
	ListPodGroupMembers(context.Context, string, string) (corev1.PodList, error)
} = &translationReader{}

func TestTranslationReaderListPodGroupMembers(t *testing.T) {
	member := func(namespace, name, group string) *corev1.Pod {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
		if group != "" {
			pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(group)}
		}
		return pod
	}
	indexer := toolscache.NewIndexer(toolscache.MetaNamespaceKeyFunc, toolscache.Indexers{podGroupMemberIndex: podGroupMemberKeys})
	for _, pod := range []*corev1.Pod{
		member("a", "w-2", "pg"),
		member("a", "w-0", "pg"),
		member("a", "w-1", "pg"),
		member("a", "other", "pg-2"),
		member("b", "w-0", "pg"),
		member("a", "loose", ""),
	} {
		if err := indexer.Add(pod); err != nil {
			t.Fatal(err)
		}
	}

	pods, err := (&translationReader{pods: indexer}).ListPodGroupMembers(context.Background(), "a", "pg")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, pod := range pods.Items {
		names = append(names, pod.Namespace+"/"+pod.Name)
	}
	if want := []string{"a/w-0", "a/w-1", "a/w-2"}; !slices.Equal(names, want) {
		t.Errorf("members = %v, want %v", names, want)
	}
}
