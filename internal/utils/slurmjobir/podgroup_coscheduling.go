// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

var (
	// Ref: https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/kep/42-podgroup-coscheduling/README.md
	podgroup_coscheduling_v1alpha1 = metav1.TypeMeta{APIVersion: "scheduling.x-k8s.io/v1alpha1", Kind: "PodGroup"}

	ErrorPodGroupCoschedulingCouldNotGet = errors.New("could not get podgroup-coscheduling")
	ErrorPodGroupCoschedulingRunning     = errors.New("PodGroup coscheduling status is Running")
	ErrorPodGroupCoschedulingUnknown     = errors.New("PodGroup coscheduling status is Unknown")
	ErrorPodGroupCoschedulingFailed      = errors.New("PodGroup coscheduling status is Failed")
	ErrorPodGroupCoschedulingFinished    = errors.New("PodGroup coscheduling status is Finished")
)

// PreFilterPodGroupCoscheduling performs PodGroup coscheduling specific PreFilter functions.
func (t *translator) PreFilterPodGroupCoscheduling(pod *corev1.Pod, slurmJobIR *SlurmJobIR) *fwk.Status {
	podGroup := &sched.PodGroup{}
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.GetNamespace(), Name: slurmJobIR.RootPOM.GetName()}
	if err := t.Get(t.ctx, key, podGroup); err != nil {
		return fwk.NewStatus(fwk.Error, ErrorPodGroupCoschedulingCouldNotGet.Error())
	}

	// If the PodGroup is in a state other than Running or Scheduling the pod will not
	// be evaluated by the SlurmBridge scheduler.
	switch podGroup.Status.Phase {
	case sched.PodGroupRunning:
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodGroupCoschedulingRunning.Error())
	case sched.PodGroupUnknown:
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodGroupCoschedulingUnknown.Error())
	case sched.PodGroupFailed:
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodGroupCoschedulingFailed.Error())
	case sched.PodGroupFinished:
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodGroupCoschedulingFinished.Error())
	}

	// Group membership, not label-propagation progress: siblings are still
	// being labeled async, so counting only already-labeled ones races that.
	if pod.Labels[wellknown.LabelExternalJobId] != "" {
		if len(slurmJobIR.AllPods()) < int(podGroup.Spec.MinMember) {
			return fwk.NewStatus(fwk.Error, ErrorExternalJobInvalid.Error())
		}
		return fwk.NewStatus(fwk.Success)
	}

	// No job yet: ensure enough unclaimed pods exist to create one.
	numPodsWaiting := 0
	for _, p := range slurmJobIR.AllPods() {
		if p.Labels[wellknown.LabelExternalJobId] == "" {
			numPodsWaiting++
		}
	}
	if numPodsWaiting < int(podGroup.Spec.MinMember) {
		return fwk.NewStatus(fwk.Error, ErrorInsuffientPods.Error())
	}
	return fwk.NewStatus(fwk.Success)
}

// GetPodGroupCoscheduling returns the PodGroup coscheduling object a Pod belongs to in cache.
func (t *translator) GetPodGroupCoscheduling(pod *corev1.Pod) (string, *sched.PodGroup) {
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

// fromPodGroupCoscheduling returns a SlurmJobIR with PodGroup coscheduling data translated.
func (t *translator) fromPodGroupCoscheduling(pod *corev1.Pod, rootPOM *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
	podGroup := &sched.PodGroup{}
	key := client.ObjectKey{Namespace: rootPOM.GetNamespace(), Name: rootPOM.GetName()}
	if err := t.Get(t.ctx, key, podGroup); err != nil {
		return nil, err
	}

	component := SlurmJobComponent{}

	if err := t.List(t.ctx, &component.Pods,
		&client.ListOptions{
			LabelSelector: labels.SelectorFromSet(
				labels.Set{sched.PodGroupLabel: pod.Labels[sched.PodGroupLabel]},
			),
			Namespace: rootPOM.Namespace,
		}); err != nil {
		return nil, err
	}

	if podGroup.Spec.MinResources.Memory().Value() != 0 {
		val := GetMemoryFromQuantity(podGroup.Spec.MinResources.Memory())
		component.JobInfo.MemPerNode = &val
	}

	if podGroup.Spec.MinResources.Cpu().Value() != 0 {
		val := int32(podGroup.Spec.MinResources.Cpu().Value()) //nolint:gosec // disable G115
		component.JobInfo.CpuPerTask = &val
	}

	if podGroup.Spec.MinMember > 0 {
		component.JobInfo.MinNodes = &podGroup.Spec.MinMember
	}

	maxNodes := int32(len(component.Pods.Items)) //nolint:gosec // disable G115
	component.JobInfo.MaxNodes = &maxNodes
	tasksPerNode := int32(1)
	component.JobInfo.TasksPerNode = &tasksPerNode

	return &SlurmJobIR{
		Components: []SlurmJobComponent{component},
	}, nil
}
