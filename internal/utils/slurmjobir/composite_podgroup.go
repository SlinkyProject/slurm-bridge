// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
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

	if _, err := snapshot.PodGroups().Get(namespace, *schedulingGroup.PodGroupName); err != nil {
		// The pod is unschedulable until its PodGroup object is created.
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("waiting for pods's pod group %q to appear in scheduling queue", *schedulingGroup.PodGroupName))
	}

	members := sets.New[types.UID]()
	for _, member := range slurmJobIR.AllPods() {
		members.Insert(member.UID)
	}
	status := t.checkCPGHierarchyReadiness(snapshot, namespace, slurmJobIR.RootPOM.Name, func(s fwk.PodGroupState) int {
		count := 0
		for uid := range s.AllPods() {
			if members.Has(uid) {
				count++
			}
		}
		return count
	})
	if status.Code() == fwk.UnschedulableAndUnresolvable {
		for _, leafPod := range slurmJobIR.AllPods() {
			if ParseSlurmJobId(leafPod.Labels[wellknown.LabelExternalJobId]) != 0 {
				ready, err := t.liveCPGTreeReady(slurmJobIR)
				if err != nil {
					return fwk.AsStatus(err)
				}
				if ready {
					return status
				}
				return fwk.NewStatus(fwk.Unschedulable, ErrorExternalJobInvalid.Error())
			}
		}
	}
	return status
}

// Confirm quorum with fresh API membership, restricted to the selected Pod UIDs.
// Independent allocations below basic boundaries are counted by the shared
// registration readiness check.
func (t *translator) liveCPGTreeReady(ir *SlurmJobIR) (bool, error) {
	fresh := *t
	fresh.podsByGroup = nil
	selected := *ir
	selected.Components = slices.Clone(ir.Components)
	for i := range selected.Components {
		component := &selected.Components[i]
		if len(component.Pods.Items) == 0 {
			continue
		}
		pods, err := fresh.podsForPodGroup(component.ObjectMeta.Namespace, component.ObjectMeta.Name)
		if err != nil {
			return false, err
		}
		live := sets.New[types.UID]()
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil {
				live.Insert(pod.UID)
			}
		}
		component.Pods.Items = slices.DeleteFunc(slices.Clone(component.Pods.Items), func(p corev1.Pod) bool { return !live.Has(p.UID) })
	}
	ready, _, err := fresh.compositeRegistrationReady(&selected)
	return ready, err
}

// checkCPGHierarchyReadiness checks the selected allocation's subtree, without
// requiring independent siblings above a basic boundary to be ready.
func (t *translator) checkCPGHierarchyReadiness(snapshot fwk.PodGroupManager, namespace, startCPGName string, readinessCountFn func(fwk.PodGroupState) int) *fwk.Status {
	cpgKey := fwk.CompositePodGroupKey(namespace, startCPGName)
	_, ok, err := snapshot.GetRootKeyForGroup(cpgKey)
	if err != nil {
		return fwk.AsStatus(err)
	}
	if !ok {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("failed to build hierarchy snapshot: composite pod group object not found in state for %s", cpgKey.String()))
	}

	if !t.isCPGTreeReady(snapshot, namespace, startCPGName, readinessCountFn) {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, fmt.Sprintf("waiting for composite pod group %q tree to meet quorum", startCPGName))
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
	// Children below a basic boundary have independent allocations.
	if cpgSpec.Spec.SchedulingPolicy.Basic != nil {
		readinessCountFn = func(s fwk.PodGroupState) int { return s.AllPodsCount() }
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
	// Every PodGroup in the namespace walks its parent chain, so look parents up by name.
	// TODO: Get parents by namespace/name once the scheduler client reads from the cache.
	byName := make(map[string]compositePodGroupInfo, len(compositePodGroups))
	for _, cpg := range compositePodGroups {
		byName[cpg.name] = cpg
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
				if found := traverseParentChain(*pg.Spec.ParentCompositePodGroupName, rootPOM.Name, byName); !found {
					continue
				}
			}

			pods, err := t.podsForPodGroup(pg.Namespace, pg.Name)
			if err != nil {
				return slurmJobIR, err
			}

			pgPOM := metav1.PartialObjectMetadata{
				TypeMeta:   t.workloadAPI.PodGroupTypeMeta,
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
func traverseParentChain(parentName string, targetName string, compositePodGroups map[string]compositePodGroupInfo) bool {
	// A chain longer than the namespace has a cycle.
	for range len(compositePodGroups) {
		compositePodGroup, ok := compositePodGroups[parentName]
		if !ok || compositePodGroup.schedulingPolicy.Basic != nil {
			return false
		}
		if compositePodGroup.parentName == targetName {
			return true
		}
		if compositePodGroup.parentName == "" {
			return false
		}
		parentName = compositePodGroup.parentName
	}
	return false
}
