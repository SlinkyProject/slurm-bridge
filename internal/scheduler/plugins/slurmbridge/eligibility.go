// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func eligibleFilterStatus(status *fwk.Status) bool {
	return status.IsSuccess() || (status.Code() == fwk.Unschedulable && status.Plugin() == Name)
}

// nodeEligibleForSlurm includes nodes whose resources are occupied by existing
// Bridge allocations. Slurm must see those nodes to queue the next allocation
// and plan backfill. An allocated pod can also wait for those resources to be
// released. canRelease, when supplied, limits removal to pods that are already
// releasing resources. Normal Kubernetes Filter and PreBind checks gate binding.
func (sb *SlurmBridge) nodeEligibleForSlurm(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, node fwk.NodeInfo, status *fwk.Status, canRelease func(*corev1.Pod) bool) (bool, *fwk.Status) {
	if status.Code() == fwk.Unschedulable && status.Plugin() == Name {
		return true, nil
	}
	if status.Code() != fwk.Unschedulable || status.Plugin() != noderesources.Name {
		return false, nil
	}

	// Pick the occupants that may be released before doing any copies or
	// extra Filter runs; nodes without them cannot become eligible.
	allocationID := podAllocationID(pod)
	var occupants []fwk.PodInfo
	for _, occupant := range node.GetPods() {
		p := occupant.GetPod()
		occupantAllocationID := podAllocationID(p)
		if p.Spec.SchedulerName != sb.schedulerName || occupantAllocationID == 0 || occupantAllocationID == allocationID {
			continue
		}
		if canRelease != nil && !canRelease(p) {
			continue
		}
		occupants = append(occupants, occupant)
	}
	if len(occupants) == 0 {
		return false, nil
	}

	// Filter stops at the first failure. Recheck the other plugins against the
	// original node so a resource failure cannot conceal a taint, volume, port,
	// or affinity constraint. Do not remove occupants for these checks.
	constraintState := state.Clone()
	constraintState.SetSkipFilterPlugins(sets.New(noderesources.Name).Union(state.GetSkipFilterPlugins()))
	constraintStatus := sb.handle.RunFilterPlugins(ctx, constraintState, pod, node)
	if constraintStatus.Code() == fwk.Error {
		return false, constraintStatus
	}
	if !eligibleFilterStatus(constraintStatus) {
		return false, nil
	}

	// Work on private copies, preserving resource use by pods without a Bridge
	// allocation. Slurm cannot account for those pods when reserving capacity.
	availableNode := node.Snapshot()
	availableState := state.Clone()
	for _, occupant := range occupants {
		if err := availableNode.RemovePod(klog.FromContext(ctx), occupant.GetPod()); err != nil {
			return false, fwk.AsStatus(err)
		}
		if status := sb.handle.RunPreFilterExtensionRemovePod(ctx, availableState, pod, occupant, availableNode); !status.IsSuccess() {
			return false, status
		}
	}

	// Retain the configured NodeResourcesFit behavior, including resource
	// requests, node capacity, ignored resources and scheduler feature gates.
	availableStatus := sb.handle.RunFilterPlugins(ctx, availableState, pod, availableNode)
	if availableStatus.Code() == fwk.Error {
		return false, availableStatus
	}
	return eligibleFilterStatus(availableStatus), nil
}

// podAllocationID identifies the whole allocation, including heterogeneous job
// components. Members of the requesting allocation cannot be treated as prior
// work that will finish before this pod binds.
func podAllocationID(pod *corev1.Pod) int32 {
	jobID := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
	if jobID <= 0 {
		return 0
	}
	if hetJobID := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalHetJobId]); hetJobID > 0 {
		return hetJobID
	}
	return jobID
}

// allocatedNodeWaitingForBridge checks for the handoff between Slurm releasing
// a previous allocation and Kubernetes removing its pods from the node cache.
// A resource failure must not tear down a gang that can bind after that handoff.
// Live allocations must still count: two partially bound gangs sharing nodes
// could otherwise wait indefinitely for one another to finish.
func (sb *SlurmBridge) allocatedNodeWaitingForBridge(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, m fwk.NodeToStatusReader) (bool, *fwk.Status) {
	nodeName := pod.Annotations[wellknown.AnnotationExternalJobNode]
	status := m.Get(nodeName)
	if status.Code() != fwk.Unschedulable || status.Plugin() != noderesources.Name {
		return false, nil
	}
	node, err := sb.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return false, fwk.AsStatus(err)
	}
	s, err := getStateData(state)
	if err != nil {
		return false, fwk.AsStatus(err)
	}
	canRelease := func(occupant *corev1.Pod) bool {
		if occupant.DeletionTimestamp != nil || occupant.Status.Phase == corev1.PodSucceeded || occupant.Status.Phase == corev1.PodFailed {
			return true
		}
		// Reuse PreFilter's pod-to-job map, but require the exact component ID:
		// another job may reference the same pod name. Missing or mismatched
		// entries are not proof that resources are being released.
		key := types.NamespacedName{Namespace: occupant.Namespace, Name: occupant.Name}
		job, ok := s.podToJob[key.String()]
		jobID := slurmjobir.ParseSlurmJobId(occupant.Labels[wellknown.LabelExternalJobId])
		return ok && job.JobId == jobID && job.Finished
	}
	return sb.nodeEligibleForSlurm(ctx, state, pod, node, status, canRelease)
}
