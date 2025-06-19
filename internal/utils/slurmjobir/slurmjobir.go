// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type SlurmJobIRJobInfo struct {
	Account      *string
	CpuPerTask   *int32
	Constraints  *string
	JobName      *string
	Licenses     *string
	MemPerNode   *int64 // memory in megabytes
	MinNodes     *int32
	MaxNodes     *int32
	Partition    *string
	QOS          *string
	Reservation  *string
	TasksPerNode *int32
	TimeLimit    *int32
	Wckey        *string
}

// Slurm Job Intermediate Representation (IR)
type SlurmJobIR struct {
	RootPOM metav1.PartialObjectMetadata
	Pods    corev1.PodList
	JobInfo SlurmJobIRJobInfo
}

type translator struct {
	client.Reader
	ctx context.Context
}

func PreFilter(c client.Client, ctx context.Context, pod *corev1.Pod, slurmJobIR *SlurmJobIR) *framework.Status {
	t := translator{Reader: c, ctx: ctx}
	switch slurmJobIR.RootPOM.TypeMeta {
	case podGroup_v1alpha1:
		return t.PreFilterPodGroup(pod, slurmJobIR)
	default:
		return framework.NewStatus(framework.Success)
	}
}

func TranslateToSlurmJobIR(c client.Client, ctx context.Context, pod *corev1.Pod) (slurmJobIR *SlurmJobIR, err error) {
	rootPOM, err := utils.GetRootOwnerMetadata(c, ctx, pod)
	if err != nil {
		return nil, err
	}

	t := translator{Reader: c, ctx: ctx}

	// PodGroup does not conventionally own the Pod, rather is associated by the PodGroupLabel.
	// The Kubernetes co-scheduler would take the PodGroup into consideration when scheduling.
	if _, podGroup := t.GetPodGroup(pod); podGroup != nil {
		rootPOM.TypeMeta = podGroup_v1alpha1
		rootPOM.Name = podGroup.Name
	}

	if err := t.Get(t.ctx, client.ObjectKeyFromObject(rootPOM), rootPOM); err != nil {
		return nil, err
	}

	switch rootPOM.TypeMeta {
	case jobSet_v1alpha2:
		slurmJobIR, err = t.fromJobSet(pod, rootPOM)
	case podGroup_v1alpha1:
		slurmJobIR, err = t.fromPodGroup(pod, rootPOM)
	case job_v1:
		slurmJobIR, err = t.fromJob(pod, rootPOM)
	case pod_v1:
		slurmJobIR, err = t.fromPod(pod)
	default:
		slurmJobIR, err = t.fromPod(pod)
	}
	if err != nil {
		return nil, err
	}
	slurmJobIR.RootPOM = *rootPOM
	err = parseAnnotations(slurmJobIR, rootPOM.Annotations)
	return slurmJobIR, err
}

func parseAnnotations(slurmJobIR *SlurmJobIR, anno map[string]string) error {
	if slurmJobIR == nil || anno == nil {
		return nil
	}

	for key, value := range anno {
		switch key {
		case wellknown.AnnotationAccount:
			slurmJobIR.JobInfo.Account = &value
		case wellknown.AnnotationConstraints:
			slurmJobIR.JobInfo.Constraints = &value
		case wellknown.AnnotationCpuPerTask:
			rs, err := resource.ParseQuantity(value)
			if err != nil {
				return err
			}
			val := int32(rs.Value())
			slurmJobIR.JobInfo.CpuPerTask = &val
		case wellknown.AnnotationJobName:
			slurmJobIR.JobInfo.JobName = &value
		case wellknown.AnnotationLicenses:
			slurmJobIR.JobInfo.Licenses = &value
		case wellknown.AnnotationMaxNodes:
			num, err := ConvStrTo32(value)
			if err != nil {
				return err
			}
			slurmJobIR.JobInfo.MaxNodes = num
		case wellknown.AnnotationMemPerNode:
			rs, err := resource.ParseQuantity(value)
			if err != nil {
				return err
			}
			val := rs.Value()
			val = val / 1048576
			slurmJobIR.JobInfo.MemPerNode = &val
		case wellknown.AnnotationMinNodes:
			num, err := ConvStrTo32(value)
			if err != nil {
				return err
			}
			slurmJobIR.JobInfo.MinNodes = num
		case wellknown.AnnotationPartition:
			slurmJobIR.JobInfo.Partition = &value
		case wellknown.AnnotationQOS:
			slurmJobIR.JobInfo.QOS = &value
		case wellknown.AnnotationReservation:
			slurmJobIR.JobInfo.Reservation = &value
		case wellknown.AnnotationTimeLimit:
			num, err := ConvStrTo32(value)
			if err != nil {
				return err
			}
			slurmJobIR.JobInfo.TimeLimit = num
		case wellknown.AnnotationWckey:
			slurmJobIR.JobInfo.Wckey = &value
		}
	}
	return nil
}
