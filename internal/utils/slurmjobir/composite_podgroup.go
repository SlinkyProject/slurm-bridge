// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrorCompositePodGroupCouldNotGet = errors.New("could not get compositepodgroup")
	ErrorCompositePodGroupUnsupported = errors.New("unsupported CompositePodGroup field")
)

// PreFilter performs CompositePodGroup specific PreFilter functions
func (t *translator) PreFilterCompositePodGroup(pod *corev1.Pod, slurmJobIR *SlurmJobIR) *fwk.Status {
	if pod.Spec.SchedulingGroup == nil {
		return nil
	}

	snapshot, err := t.handle.PodGroupManager().BuildHierarchySnapshotFromPod(pod)
	if err != nil {
		// Could not build snapshot (e.g. root PG not found). Treat as unschedulable.
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("failed to build hierarchy snapshot: %v", err))
	}

	namespace := pod.Namespace
	schedulingGroup := pod.Spec.SchedulingGroup

	podGroup, err := snapshot.PodGroups().Get(namespace, *schedulingGroup.PodGroupName)
	if err != nil {
		// The pod is unschedulable until its PodGroup object is created.
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("waiting for pods's pod group %q to appear in scheduling queue", *schedulingGroup.PodGroupName))
	}

	return t.checkCPGHierarchyReadiness(snapshot, namespace, *podGroup.Spec.ParentCompositePodGroupName, func(s fwk.PodGroupState) int { return s.AllPodsCount() })
}

// checkCPGHierarchyReadiness checks if the Composite Pod Group hierarchy is ready for scheduling.
// It first retrieves the root composite pod group and then recursively traverses the entire Composite Pod Group hierarchy
// to determine if the hierarchy is ready for scheduling.
func (t *translator) checkCPGHierarchyReadiness(snapshot fwk.PodGroupManager, namespace, startCPGName string, readinessCountFn func(fwk.PodGroupState) int) *fwk.Status {
	cpgKey := fwk.CompositePodGroupKey(namespace, startCPGName)
	rootKey, ok, err := snapshot.GetRootKeyForGroup(cpgKey)
	if err != nil {
		return fwk.AsStatus(err)
	}
	if !ok {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("failed to build hierarchy snapshot: composite pod group object not found in state for %s", cpgKey.String()))
	}

	if !t.isCPGTreeReady(snapshot, rootKey.Namespace, rootKey.Name, readinessCountFn) {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("waiting for composite pod group %q tree to meet quorum", rootKey.Name))
	}
	return nil
}

func (t *translator) isCPGTreeReady(snapshot fwk.PodGroupManager, namespace, cpgName string, readinessCountFn func(fwk.PodGroupState) int) bool {
	cpgState, err := snapshot.CompositePodGroupStates().Get(namespace, cpgName)
	if err != nil {
		return false
	}

	cpgSpec, err := snapshot.CompositePodGroups().Get(namespace, cpgName)
	if err != nil {
		return false
	}
	minGroupCount := 1
	policy := cpgSpec.Spec.SchedulingPolicy
	if policy.Gang != nil {
		minGroupCount = int(policy.Gang.MinGroupCount)
	}

	successfulChildren := 0
	for _, childKey := range cpgState.GetChildren() {
		childType, _, childName := childKey.Type, childKey.Namespace, childKey.Name
		if childType == fwk.CompositePodGroupKeyType {
			if t.isCPGTreeReady(snapshot, namespace, childName, readinessCountFn) {
				successfulChildren++
			}
		} else {
			if t.isPGReady(snapshot, namespace, childName, readinessCountFn) {
				successfulChildren++
			}
		}
	}

	return successfulChildren >= minGroupCount
}

func (t *translator) isPGReady(snapshot fwk.PodGroupManager, namespace, pgName string, readinessCountFn func(fwk.PodGroupState) int) bool {
	pg, err := snapshot.PodGroups().Get(namespace, pgName)
	if err != nil {
		return false
	}

	minCount := 1
	if pg.Spec.SchedulingPolicy.Gang != nil {
		minCount = int(pg.Spec.SchedulingPolicy.Gang.MinCount)
	}

	pgState, err := snapshot.PodGroupStates().Get(namespace, pgName)
	if err != nil {
		return false
	}

	return readinessCountFn(pgState) >= minCount
}

// fromCompositePodGroup will translate from a CompositePodGroup into a SlurmJobIR
func (t *translator) fromCompositePodGroup(pod *corev1.Pod, rootPOM *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
	key := client.ObjectKey{Namespace: rootPOM.GetNamespace(), Name: rootPOM.GetName()}
	if _, err := t.getCompositePodGroup(key); err != nil {
		return nil, err
	}

	compositePodGroups, err := t.listCompositePodGroup(rootPOM.GetNamespace())
	if err != nil {
		return nil, err

	}

	slurmJobIR := new(SlurmJobIR)

	podGroups := &PodGroupList{TypeMeta: metav1.TypeMeta{
		APIVersion: t.workloadAPI.PodGroupTypeMeta.APIVersion,
		Kind:       "PodGroupList",
	}}
	if err := t.List(t.ctx, podGroups, client.InNamespace(rootPOM.GetNamespace())); err != nil {
		return slurmJobIR, err
	}

	for _, pg := range podGroups.Items {
		if pg.Spec.ParentCompositePodGroupName != nil {

			if *pg.Spec.ParentCompositePodGroupName != rootPOM.GetName() {
				if found := t.traverseParentChain(rootPOM.Namespace, *pg.Spec.ParentCompositePodGroupName, rootPOM.Name, compositePodGroups, 0); !found {
					continue
				}
			}

			pods, err := t.podsForPodGroup(pg.Namespace, pg.Name)
			if err != nil {
				return slurmJobIR, err
			}

			pgPOM := metav1.PartialObjectMetadata{
				TypeMeta:   pg.TypeMeta,
				ObjectMeta: pg.ObjectMeta,
			}

			firstPod := getFirstPod(pods)
			if firstPod != nil {
				pgIR, err := t.fromPodGroup(firstPod, &pgPOM)
				if err != nil {
					return slurmJobIR, err
				}
				slurmJobIR.Components = append(slurmJobIR.Components, pgIR.Components...)
			}
		}
	}

	return slurmJobIR, nil
}

// traverseParentChain follows each CompositePodGroup's parent up until it reaches the root
// object, then returns the name of that object
func (t *translator) traverseParentChain(namespace string, parentName string, targetName string, compositePodGroups []compositePodGroupInfo, depth int) bool {
	if depth == len(compositePodGroups) {
		return false
	}
	for _, compositePodGroup := range compositePodGroups {
		if compositePodGroup.name != parentName {
			continue
		}
		if compositePodGroup.parentName == targetName {
			return true
		}
		if compositePodGroup.parentName == "" {
			return false
		}
		return t.traverseParentChain(namespace, compositePodGroup.parentName, targetName, compositePodGroups, depth+1)
	}
	return false
}
