// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

var (
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

// ValidatePodGroupSupport rejects built-in PodGroup references when the bridge
// feature is disabled. Legacy scheduler-plugins PodGroups remain supported.
func ValidatePodGroupSupport(api *WorkloadAPI, pod *corev1.Pod) error {
	if _, grouped := podGroupName(pod); grouped && api == nil {
		return fmt.Errorf("pod %s/%s uses spec.schedulingGroup but built-in Workload support is disabled; enable --feature-gates=%s=true on a cluster serving a supported Workload and PodGroup API", pod.Namespace, pod.Name, features.SlurmBridgeGenericWorkload)
	}
	return nil
}

// parsePodGroupSlurmAnnotations merges Slurm annotations from the PodGroup,
// selected controller, and Workload. Only one controller source is applied.
// Ref: https://kubernetes.io/docs/concepts/workloads/podgroup-api/
func (t *translator) parsePodGroupSlurmAnnotations(
	slurmJobComponent *SlurmJobComponent,
	pg *PodGroup,
	controllerPOM *metav1.PartialObjectMetadata,
) error {
	if err := parseUserAnnotations(slurmJobComponent, pg.GetAnnotations()); err != nil {
		return err
	}
	if controllerPOM != nil {
		ann := controllerPOM.GetAnnotations()
		if controllerPOM.Kind == "Job" {
			job := &batchv1.Job{}
			if err := t.Get(t.ctx, client.ObjectKeyFromObject(controllerPOM), job); err == nil {
				ann = job.GetAnnotations()
			}
		} else if controllerPOM.TypeMeta == jobSet_v1alpha2 {
			jobSet := &jobset.JobSet{}
			if err := t.Get(t.ctx, client.ObjectKeyFromObject(controllerPOM), jobSet); err == nil {
				ann = jobSet.GetAnnotations()
			}
		}
		if err := parseUserAnnotations(slurmJobComponent, ann); err != nil {
			return err
		}
	}
	workloadName := pg.workloadName()
	if workloadName == "" {
		return nil
	}
	key := client.ObjectKey{Namespace: pg.GetNamespace(), Name: workloadName}
	wl := &Workload{TypeMeta: metav1.TypeMeta{APIVersion: t.workloadAPI.PodGroupTypeMeta.APIVersion, Kind: "Workload"}}
	if err := t.Get(t.ctx, key, wl); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return parseUserAnnotations(slurmJobComponent, wl.GetAnnotations())
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
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.GetNamespace(), Name: slurmJobIR.RootPOM.GetName()}
	pg := &PodGroup{TypeMeta: t.workloadAPI.PodGroupTypeMeta}
	if err := t.Get(t.ctx, key, pg); err != nil {
		return fwk.NewStatus(fwk.Error, ErrorPodGroupCouldNotGet.Error())
	}
	minCount := pg.gangMinCount()
	if minCount == nil {
		return fwk.NewStatus(fwk.Success)
	}
	var numPodsWaiting int32
	for _, p := range slurmJobIR.AllPods() {
		if p.Labels[wellknown.LabelExternalJobId] == pod.Labels[wellknown.LabelExternalJobId] {
			numPodsWaiting++
		}
	}
	if numPodsWaiting < *minCount {
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
	slurmJobIR := new(SlurmJobIR)
	slurmJobComponent := new(SlurmJobComponent)

	ref := pod.Spec.SchedulingGroup
	for i := range allPods.Items {
		p := &allPods.Items[i]
		if p.Spec.SchedulingGroup == nil {
			continue
		}
		if schedulingGroupsMatch(p.Spec.SchedulingGroup, ref) {
			slurmJobComponent.Pods.Items = append(slurmJobComponent.Pods.Items, *p)
		}
	}
	if len(slurmJobComponent.Pods.Items) == 0 {
		return nil, ErrorPodGroupNoPods
	}

	slurmJobComponent.JobInfo.JobName = ptr.To(rootPOM.Name)
	n := int32(len(slurmJobComponent.Pods.Items)) //nolint:gosec // count bounded by cluster
	slurmJobComponent.JobInfo.MinNodes = ptr.To(n)
	slurmJobComponent.JobInfo.MaxNodes = ptr.To(n)
	slurmJobComponent.JobInfo.TasksPerNode = ptr.To(int32(1))

	slurmJobIR.Components = []SlurmJobComponent{
		*slurmJobComponent,
	}

	return slurmJobIR, nil
}
