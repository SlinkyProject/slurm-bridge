// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

var ErrLWSLeaderMissing = errors.New("waiting for the LWS replica group's leader")

// TODO: Replace this fallback with native PodGroup identity once LWS
// WorkloadAwareScheduling is required. Each legacy LWS replica group has its
// own allocation; the LeaderWorkerSet UID alone does not identify that group.
func lwsAllocationIdentity(pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) (AllocationIdentity, error) {
	hash := pod.Labels[lwsv1.GroupUniqueHashLabelKey]
	for _, member := range ir.AllPods() {
		if hash != "" && member.Labels[lwsv1.GroupUniqueHashLabelKey] == hash && member.Labels[lwsv1.WorkerIndexLabelKey] == "0" {
			return podAllocationIdentity(&member), nil
		}
	}
	return AllocationIdentity{}, ErrLWSLeaderMissing
}
