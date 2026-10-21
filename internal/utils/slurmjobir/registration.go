// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
)

// RegistrationReady applies workload quorum checks without a scheduler handle.
func RegistrationReady(c client.Client, registry *dra.Registry, api *WorkloadAPI, ctx context.Context, pod *corev1.Pod, ir *SlurmJobIR) (bool, error) {
	if err := ir.Validate(); err != nil {
		return false, err
	}
	if ir.RootPOM.TypeMeta != compositePodGroupV1Alpha3 {
		status := PreFilter(c, registry, nil, api, ctx, pod, ir)
		if status.IsSuccess() {
			return true, nil
		}
		if status.IsError() {
			return false, status.AsError()
		}
		return false, nil
	}
	t := translator{Reader: c, ctx: ctx, workloadAPI: api}
	groups, err := t.listCompositePodGroup(pod.Namespace)
	if err != nil {
		return false, err
	}
	leaves := &PodGroupList{TypeMeta: metav1.TypeMeta{APIVersion: api.PodGroupTypeMeta.APIVersion, Kind: "PodGroupList"}}
	if err := c.List(ctx, leaves, client.InNamespace(pod.Namespace)); err != nil {
		return false, err
	}
	var ready func(string, int) (bool, error)
	ready = func(name string, depth int) (bool, error) {
		if depth > maxCompositePodGroupTreeDepth {
			return false, fmt.Errorf("CompositePodGroup hierarchy exceeds maximum depth")
		}
		group, err := t.getCompositePodGroup(client.ObjectKey{Namespace: pod.Namespace, Name: name})
		if err != nil {
			return false, err
		}
		children := int64(0)
		for _, child := range groups {
			if child.parentName != name {
				continue
			}
			ok, err := ready(child.name, depth+1)
			if err != nil {
				return false, err
			}
			if ok {
				children++
			}
		}
		for _, leaf := range leaves.Items {
			if ptr.Deref(leaf.Spec.ParentCompositePodGroupName, "") != name {
				continue
			}
			if err := validatePodGroupSpec(&leaf); err != nil {
				return false, err
			}
			pods, err := t.podsForPodGroup(leaf.Namespace, leaf.Name)
			if err != nil {
				return false, err
			}
			minCount := ptr.Deref(leaf.gangMinCount(), 1)
			count := int32(0)
			for _, p := range pods.Items {
				if p.DeletionTimestamp == nil && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
					count++
				}
			}
			if count >= minCount {
				children++
			}
		}
		minGroupCount := int64(1)
		if group.schedulingPolicy.Gang != nil {
			minGroupCount = int64(group.schedulingPolicy.Gang.MinGroupCount)
		}
		return children >= minGroupCount, nil
	}
	return ready(ir.RootPOM.Name, 1)
}
