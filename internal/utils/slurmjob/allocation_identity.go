// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

type AllocationIdentity struct {
	Index, Key string
	// PodGroup allocations created before controller registration have only
	// Pod ownership metadata. Keep these lookup keys separate from the group index key.
	legacyPodKeys []string
}

func podAllocationIdentity(pod *corev1.Pod) AllocationIdentity {
	key := externaljobinfo.PodKey(pod)
	return AllocationIdentity{Index: PodJobIndex, Key: key}
}

func IdentityForAllocation(pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) (AllocationIdentity, error) {
	if group := externaljobinfo.NewGroupIdentity(ir.RootPOM); group != nil {
		identity := AllocationIdentity{Index: GroupJobIndex, Key: group.Key()}
		// CompositePodGroups were not released before group identity metadata.
		if ir.RootPOM.Kind == "PodGroup" {
			for _, member := range ir.AllPods() {
				identity.legacyPodKeys = append(identity.legacyPodKeys, externaljobinfo.PodKey(&member))
				if member.UID != "" {
					identity.legacyPodKeys = append(identity.legacyPodKeys, member.Namespace+"/"+member.Name)
				}
			}
		}
		return identity, nil
	}
	if ir.RootPOM.Kind == "LeaderWorkerSet" {
		return lwsAllocationIdentity(pod, ir)
	}
	return podAllocationIdentity(pod), nil
}
