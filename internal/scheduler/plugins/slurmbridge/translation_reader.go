// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const podGroupMemberIndex = "slurmbridge-podgroup-member"

// translationReader serves PodGroup members from the scheduler's Pod informer, so
// translation does not list every Pod in the namespace on each attempt.
type translationReader struct {
	client.Client
	pods cache.Indexer
}

func podGroupMemberKeys(obj interface{}) ([]string, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
		return nil, nil
	}
	return []string{pod.Namespace + "/" + *pod.Spec.SchedulingGroup.PodGroupName}, nil
}

func newTranslationReader(c client.Client, pods cache.SharedIndexInformer) (*translationReader, error) {
	if _, exists := pods.GetIndexer().GetIndexers()[podGroupMemberIndex]; !exists {
		if err := pods.AddIndexers(cache.Indexers{podGroupMemberIndex: podGroupMemberKeys}); err != nil {
			return nil, fmt.Errorf("index Pods by PodGroup: %w", err)
		}
	}
	return &translationReader{Client: c, pods: pods.GetIndexer()}, nil
}

// ListPodGroupMembers returns non-terminal members, the same set the scheduler's
// Pod informer holds, ordered by name like an apiserver List.
func (r *translationReader) ListPodGroupMembers(_ context.Context, namespace, name string) (corev1.PodList, error) {
	objs, err := r.pods.ByIndex(podGroupMemberIndex, namespace+"/"+name)
	if err != nil {
		return corev1.PodList{}, err
	}
	pods := corev1.PodList{Items: make([]corev1.Pod, 0, len(objs))}
	for _, obj := range objs {
		pods.Items = append(pods.Items, *obj.(*corev1.Pod).DeepCopy())
	}
	slices.SortFunc(pods.Items, func(a, b corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	return pods, nil
}
