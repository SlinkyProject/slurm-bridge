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
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

// RegistrationMembers uses the same membership traversal as translation, without
// resolving device resources or building submission descriptions. The caller has
// already checked the root UID; replicaHash distinguishes legacy LWS replicas.
func RegistrationMembers(c client.Reader, workloadAPI *WorkloadAPI, ctx context.Context, root *metav1.PartialObjectMetadata, replicaHash string) ([]corev1.Pod, error) {
	// Keep terminal members available for allocation identity and duplicate cleanup.
	// TranslateToSlurmJobIR separately selects the Pods that still need resources.
	t := translator{Reader: c, ctx: ctx, workloadAPI: workloadAPI, includeTerminalPods: true}
	seed := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: root.Namespace}}
	var ir *SlurmJobIR
	var err error
	switch {
	case isBuiltInPodGroup(root.TypeMeta):
		seed.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(root.Name)}
		ir, err = t.fromPodGroup(seed, root)
	case root.TypeMeta == compositePodGroupV1Alpha3:
		ir, err = t.fromCompositePodGroup(seed, root)
	case root.TypeMeta == podgroup_coscheduling_v1alpha1:
		seed.Labels = map[string]string{sched.PodGroupLabel: root.Name}
		ir, err = t.fromPodGroupCoscheduling(seed, root)
	case root.TypeMeta == lws_v1:
		seed.Labels = map[string]string{lwsv1.GroupUniqueHashLabelKey: replicaHash}
		ir, err = t.fromLws(seed, root)
	default:
		return nil, fmt.Errorf("unsupported registration root %s", root.Kind)
	}
	if err != nil {
		return nil, err
	}
	return ir.AllPods(), nil
}
