// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// Ref: https://kubernetes.io/docs/concepts/workloads/podgroup-api/
	podgroup_v1alpha2 = metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha2", Kind: "PodGroup"}

	ErrorPodGroupCouldNotGet = errors.New("could not get podgroup")
	ErrorPodGroupNoPods      = errors.New("no pods for scheduling group found")
)

func podGroupName(pod *corev1.Pod) (string, bool) {
	if pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
		return "", false
	}
	name := *pod.Spec.SchedulingGroup.PodGroupName
	return name, name != ""
}

func schedulingGroupsMatch(a, b *corev1.PodSchedulingGroup) bool {
	if a == nil || b == nil {
		return false
	}
	if a.PodGroupName == nil || b.PodGroupName == nil {
		return false
	}
	return *a.PodGroupName == *b.PodGroupName
}

// PreFilterPodGroup enforces gang scheduling MinCount (and external-job consistency)
// for pods that reference a scheduling.k8s.io PodGroup via spec.schedulingGroup.
func (t *translator) PreFilterPodGroup(pod *corev1.Pod, slurmJobIR *SlurmJobIR) *fwk.Status {
	pg := &schedulingv1alpha2.PodGroup{}
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.GetNamespace(), Name: slurmJobIR.RootPOM.GetName()}
	if err := t.Get(t.ctx, key, pg); err != nil {
		return fwk.NewStatus(fwk.Error, ErrorPodGroupCouldNotGet.Error())
	}
	if pg.Spec.SchedulingPolicy.Gang == nil {
		return fwk.NewStatus(fwk.Success)
	}
	minCount := pg.Spec.SchedulingPolicy.Gang.MinCount
	var numPodsWaiting int32
	for _, p := range slurmJobIR.Pods.Items {
		if p.Labels[wellknown.LabelExternalJobId] == pod.Labels[wellknown.LabelExternalJobId] {
			numPodsWaiting++
		}
	}
	if numPodsWaiting < minCount {
		if pod.Labels[wellknown.LabelExternalJobId] == "" {
			return fwk.NewStatus(fwk.Error, ErrorInsuffientPods.Error())
		}
		return fwk.NewStatus(fwk.Error, ErrorExternalJobInvalid.Error())
	}
	return fwk.NewStatus(fwk.Success)
}

// fromPodGroup builds SlurmJobIR for pods with spec.schedulingGroup.podGroupName set.
func (t *translator) fromPodGroup(pod *corev1.Pod, rootPOM *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
	var allPods corev1.PodList
	if err := t.List(t.ctx, &allPods, client.InNamespace(pod.Namespace)); err != nil {
		return nil, err
	}
	slurmJobIR := &SlurmJobIR{}
	ref := pod.Spec.SchedulingGroup
	for i := range allPods.Items {
		p := &allPods.Items[i]
		if p.Spec.SchedulingGroup == nil {
			continue
		}
		if schedulingGroupsMatch(p.Spec.SchedulingGroup, ref) {
			slurmJobIR.Pods.Items = append(slurmJobIR.Pods.Items, *p)
		}
	}
	if len(slurmJobIR.Pods.Items) == 0 {
		return nil, ErrorPodGroupNoPods
	}

	slurmJobIR.JobInfo.JobName = ptr.To(rootPOM.Name)
	n := int32(len(slurmJobIR.Pods.Items)) //nolint:gosec // count bounded by cluster
	slurmJobIR.JobInfo.MinNodes = ptr.To(n)
	slurmJobIR.JobInfo.MaxNodes = ptr.To(n)
	slurmJobIR.JobInfo.TasksPerNode = ptr.To(int32(1))

	return slurmJobIR, nil
}
