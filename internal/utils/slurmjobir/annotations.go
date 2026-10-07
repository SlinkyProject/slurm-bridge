// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/timelimit"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// applySlurmAnnotations applies root annotations or merges PodGroup, controller,
// and Workload annotations when the pod belongs to a built-in PodGroup.
func (t *translator) applySlurmAnnotations(
	ctx context.Context,
	slurmJobIR *SlurmJobIR,
	rootPOM *metav1.PartialObjectMetadata,
	pg *PodGroup,
) error {
	if pg == nil {
		for i := range slurmJobIR.Components {
			if err := parseUserAnnotations(&slurmJobIR.Components[i], rootPOM.Annotations); err != nil {
				return err
			}
		}
		return nil
	}

	controllerPOM := rootPOM

	for i := range slurmJobIR.Components {
		firstPod := getFirstPod(slurmJobIR.Components[i].Pods)
		if firstPod != nil {
			if pg.Spec.SchedulingPolicy.Gang != nil || len(slurmJobIR.Components) > 1 {
				c, ok := t.Reader.(client.Client)
				if !ok {
					return fmt.Errorf("client does not support owner metadata lookup")
				}
				var err error
				controllerPOM, err = getRootOwnerMetadata(c, t.ctx, firstPod)
				if err != nil {
					return err
				}
			}
			if pgName, ok := podGroupName(firstPod); ok {
				if err := t.Get(ctx, client.ObjectKey{Namespace: firstPod.Namespace, Name: pgName}, pg); err != nil {
					return err
				}
			}
			if err := t.parsePodGroupSlurmAnnotations(&slurmJobIR.Components[i], pg, controllerPOM); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseUserAnnotations(slurmJobComponent *SlurmJobComponent, anno map[string]string) error {
	if slurmJobComponent == nil || anno == nil {
		return nil
	}

	for key, value := range anno {
		switch key {
		case wellknown.AnnotationAccount:
			slurmJobComponent.JobInfo.Account = &value
		case wellknown.AnnotationConstraints:
			slurmJobComponent.JobInfo.Constraints = &value
		case wellknown.AnnotationGres:
			slurmJobComponent.JobInfo.Gres = &value
		case wellknown.AnnotationGroupId:
			slurmJobComponent.JobInfo.GroupId = &value
		case wellknown.AnnotationCpuPerTask:
			rs, err := resource.ParseQuantity(value)
			if err != nil {
				return err
			}
			val := int32(rs.Value()) //nolint:gosec // disable G115
			slurmJobComponent.JobInfo.CpuPerTask = &val
		case wellknown.AnnotationExclusive:
			v := strings.TrimSpace(strings.ToLower(value))
			exclusive := v != "false"
			slurmJobComponent.JobInfo.Exclusive = &exclusive
		case wellknown.AnnotationJobName:
			slurmJobComponent.JobInfo.JobName = &value
		case wellknown.AnnotationLicenses:
			slurmJobComponent.JobInfo.Licenses = &value
		case wellknown.AnnotationMaxNodes:
			num, err := ConvStrTo32(value)
			if err != nil {
				return err
			}
			slurmJobComponent.JobInfo.MaxNodes = num
		case wellknown.AnnotationMemPerNode:
			rs, err := resource.ParseQuantity(value)
			if err != nil {
				return err
			}
			val := rs.Value()
			val /= 1048576 // value for 1024x1024 to follow what we need for slurm job IR
			slurmJobComponent.JobInfo.MemPerNode = &val
		case wellknown.AnnotationMinNodes:
			num, err := ConvStrTo32(value)
			if err != nil {
				return err
			}
			slurmJobComponent.JobInfo.MinNodes = num
		case wellknown.AnnotationPartition:
			slurmJobComponent.JobInfo.Partition = &value
		case wellknown.AnnotationPriority:
			num, err := ConvStrTo32(value)
			if err != nil {
				return err
			}
			slurmJobComponent.JobInfo.Priority = num
		case wellknown.AnnotationQOS:
			slurmJobComponent.JobInfo.QOS = &value
		case wellknown.AnnotationReservation:
			slurmJobComponent.JobInfo.Reservation = &value
		case wellknown.AnnotationTimeLimit:
			minutes, err := timelimit.Parse(value)
			if err != nil {
				return err
			}
			slurmJobComponent.JobInfo.TimeLimit = &minutes
		case wellknown.AnnotationUserId:
			slurmJobComponent.JobInfo.UserId = &value
		case wellknown.AnnotationWckey:
			slurmJobComponent.JobInfo.Wckey = &value
		}
	}
	return nil
}
