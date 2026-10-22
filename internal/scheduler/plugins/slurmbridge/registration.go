// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	lws "sigs.k8s.io/lws/api/leaderworkerset/v1"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// The existing Pod informer reports each Pod on addition, including startup
// replay. Updates do not repeat this event. Use only Pod metadata so diagnostics
// do not require extra reads or depend on the group's existence or readiness.
func (sb *SlurmBridge) recordRegistrationPending(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.Spec.SchedulerName != sb.schedulerName || pod.Spec.NodeName != "" || pod.DeletionTimestamp != nil {
		return
	}
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return
	}
	if !slices.ContainsFunc(pod.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool {
		return g.Name == wellknown.SchedulingGateRegistration
	}) {
		return
	}
	var kind, name, group, parentHint string
	switch {
	case pod.Spec.SchedulingGroup != nil && ptr.Deref(pod.Spec.SchedulingGroup.PodGroupName, "") != "":
		kind, name, group = "PodGroup", *pod.Spec.SchedulingGroup.PodGroupName, "scheduling.k8s.io"
		parentHint = "; for composite workloads, follow its parent CompositePodGroup"
	case pod.Labels[sched.PodGroupLabel] != "":
		kind, name, group = "PodGroup", pod.Labels[sched.PodGroupLabel], sched.SchemeGroupVersion.Group
	case pod.Labels[lws.SetNameLabelKey] != "":
		kind, name, group = "LeaderWorkerSet", pod.Labels[lws.SetNameLabelKey], lws.GroupVersion.Group
	default:
		return
	}
	message := fmt.Sprintf("Waiting for Slurm registration; see %s %s/%s (%s) for registration events%s",
		kind, pod.Namespace, name, group, parentHint)
	sb.handle.EventRecorder().Eventf(pod, nil, corev1.EventTypeNormal, "SlurmRegistrationPending", "WaitForRegistration", "%s", message)
}
