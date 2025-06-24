// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

var (
	// Ref: https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/kep/42-podgroup-coscheduling/README.md
	podGroup_v1alpha1 = metav1.TypeMeta{APIVersion: "scheduling.x-k8s.io/v1alpha1", Kind: "PodGroup"}

	ErrorCouldNotGetPodGroup   = errors.New("could not get podgroup")
	ErrorInsuffientPods        = errors.New("not enough pending pods to satisfy MinMember")
	ErrorPlaceholderJobInvalid = errors.New("not enough pending pods to satisfy MinMembers for Placeholder job")
	ErrorPodGroupRunning       = errors.New("PodGroup status is Running")
	ErrorPodGroupUnknown       = errors.New("PodGroup status is Unknown")
	ErrorPodGroupFailed        = errors.New("PodGroup status is Failed")
	ErrorPodGroupFinished      = errors.New("PodGroup status is Finished")
)

// PreFilter performs PodGroup specific PreFilter functions
func (t *translator) PreFilterPodGroup(pod *corev1.Pod, slurmJobIR *SlurmJobIR) *framework.Status {
	podGroup := &sched.PodGroup{}
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.GetNamespace(), Name: slurmJobIR.RootPOM.GetName()}
	if err := t.Get(t.ctx, key, podGroup); err != nil {
		return framework.NewStatus(framework.Error, ErrorCouldNotGetPodGroup.Error())
	}

	// If the PodGroup is in a state other than Running or Scheduling the pod will not
	// be evaluated by the SlurmBridge scheduler.
	switch podGroup.Status.Phase {
	case sched.PodGroupRunning:
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupRunning.Error())
	case sched.PodGroupUnknown:
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupUnknown.Error())
	case sched.PodGroupFailed:
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupFailed.Error())
	case sched.PodGroupFinished:
		return framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupFinished.Error())
	}

	// Ensure there are enough pods to satisfy MinMembers. Don't count pods
	// that may already have a placeholderjob annotation.
	numPodsWaiting := 0
	for _, p := range slurmJobIR.Pods.Items {
		if p.Labels[wellknown.LabelPlaceholderJobId] ==
			pod.Labels[wellknown.LabelPlaceholderJobId] {
			numPodsWaiting++
		}
	}

	// If the pod has no placeholder job return an error to wait for more to be created.
	// If the pod had a placeholder job and now MinMember can no longer be satisfied because
	// one or more pods were deleted after submitting the placeholder job, return an error
	// to indicate placeholder job cleanup must occur.
	if numPodsWaiting < int(podGroup.Spec.MinMember) {
		if pod.Labels[wellknown.LabelPlaceholderJobId] == "" {
			return framework.NewStatus(framework.Error, ErrorInsuffientPods.Error())
		} else {
			return framework.NewStatus(framework.Error, ErrorPlaceholderJobInvalid.Error())
		}
	}
	return framework.NewStatus(framework.Success)
}

// GetPodGroup returns the PodGroup that a Pod belongs to in cache.
func (t *translator) GetPodGroup(pod *corev1.Pod) (string, *sched.PodGroup) {
	pgName := pod.Labels[sched.PodGroupLabel]
	if len(pgName) == 0 {
		return "", nil
	}
	pg := &sched.PodGroup{}
	key := types.NamespacedName{Namespace: pod.Namespace, Name: pgName}
	if err := t.Get(t.ctx, key, pg); err != nil {
		return key.String(), nil
	}
	return key.String(), pg
}

// fromPodGroup will return a SlurmJobIR with the relevant PodGroup data translated
func (t *translator) fromPodGroup(pod *corev1.Pod, rootPOM *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
	podGroup := &sched.PodGroup{}
	key := client.ObjectKey{Namespace: rootPOM.GetNamespace(), Name: rootPOM.GetName()}
	if err := t.Get(t.ctx, key, podGroup); err != nil {
		return nil, err
	}

	slurmJobIR := &SlurmJobIR{}

	if err := t.List(t.ctx, &slurmJobIR.Pods,
		&client.ListOptions{LabelSelector: labels.SelectorFromSet(
			labels.Set{sched.PodGroupLabel: pod.Labels[sched.PodGroupLabel]},
		)}); err != nil {
		return nil, err
	}

	if podGroup.Spec.MinResources.Memory().Value() != 0 {
		val := GetMemoryFromQuantity(podGroup.Spec.MinResources.Memory())
		slurmJobIR.JobInfo.MemPerNode = &val
	}

	if podGroup.Spec.MinResources.Cpu().Value() != 0 {
		val := int32(podGroup.Spec.MinResources.Cpu().Value()) //nolint:gosec // disable G115
		slurmJobIR.JobInfo.CpuPerTask = &val
	}

	if podGroup.Spec.MinMember > 0 {
		slurmJobIR.JobInfo.MinNodes = &podGroup.Spec.MinMember
	}

	maxNodes := int32(len(slurmJobIR.Pods.Items)) //nolint:gosec // disable G115
	slurmJobIR.JobInfo.MaxNodes = &maxNodes
	tasksPerNode := int32(1)
	slurmJobIR.JobInfo.TasksPerNode = &tasksPerNode

	return slurmJobIR, nil
}
