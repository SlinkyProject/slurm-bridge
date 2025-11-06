// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resourcehelper "k8s.io/component-helpers/resource"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const (
	nvidiaDevicePlugin = "nvidia.com/gpu"
	amdDevicePlugin    = "amd.com/gpu"
)

type SlurmJobIRJobInfo struct {
	Account      *string
	CpuPerTask   *int32
	Constraints  *string
	Gres         *string
	GroupId      *string
	JobName      *string
	Licenses     *string
	MemPerNode   *int64 // memory in megabytes
	MinNodes     *int32
	MaxNodes     *int32
	Nodes        []string
	Partition    *string
	QOS          *string
	Reservation  *string
	TasksPerNode *int32
	TimeLimit    *int32
	UserId       *string
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

func PreFilter(c client.Client, ctx context.Context, pod *corev1.Pod, slurmJobIR *SlurmJobIR) *fwk.Status {
	t := translator{Reader: c, ctx: ctx}
	switch slurmJobIR.RootPOM.TypeMeta {
	case podGroup_v1alpha1:
		return t.PreFilterPodGroup(pod, slurmJobIR)
	case lws_v1:
		return t.PreFilterLWS(pod, slurmJobIR)
	default:
		return fwk.NewStatus(fwk.Success)
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
	case lws_v1:
		slurmJobIR, err = t.fromLws(pod, rootPOM)
	default:
		slurmJobIR, err = t.fromPod(pod)
	}
	if err != nil {
		return nil, err
	}
	slurmJobIR.RootPOM = *rootPOM
	parsePodsCpuAndMemory(slurmJobIR)
	parseGPUDevicePlugin(slurmJobIR)
	err = parseAnnotations(slurmJobIR, rootPOM.Annotations)
	return slurmJobIR, err
}

/* Set CPU and Memory for the placeholder job based on the maximum Pod CPU and Memory (including overhead) */
func parsePodsCpuAndMemory(slurmJobIR *SlurmJobIR) {
	var cpuMax resource.Quantity
	var memMax resource.Quantity
	for _, p := range slurmJobIR.Pods.Items {
		lim := resourcehelper.PodLimits(&p, resourcehelper.PodResourcesOptions{})
		req := resourcehelper.PodRequests(&p, resourcehelper.PodResourcesOptions{})
		if req.Cpu().Cmp(cpuMax) == 1 {
			cpuMax = *req.Cpu()
		}
		if lim.Cpu().Cmp(cpuMax) == 1 {
			cpuMax = *lim.Cpu()
		}
		if req.Memory().Cmp(memMax) == 1 {
			memMax = *req.Memory()
		}
		if lim.Memory().Cmp(memMax) == 1 {
			memMax = *lim.Memory()
		}
	}
	// If either CPU or Memory is set to 0, leave that value unset so Slurm
	// will use the default values of the partition. Slurm does not support
	// unbounded cpu or memory.
	if cpuMax.Value() > 0 {
		slurmJobIR.JobInfo.CpuPerTask = ptr.To(int32(cpuMax.Value())) //nolint:gosec
	}
	if memMax.Value() > 0 {
		slurmJobIR.JobInfo.MemPerNode = ptr.To(GetMemoryFromQuantity(&memMax))
	}
}

/* Set GRES for the placeholder job to the maximum quantity of GPUs requested */
func parseGPUDevicePlugin(slurmJobIR *SlurmJobIR) {
	var gres string
	var gresMax resource.Quantity
	for _, p := range slurmJobIR.Pods.Items {
		lim := resourcehelper.PodLimits(&p, resourcehelper.PodResourcesOptions{})
		for resourceName, quantity := range lim {
			if resourceName == nvidiaDevicePlugin || resourceName == amdDevicePlugin {
				if quantity.Cmp(gresMax) > 0 {
					gresMax = quantity
					gres = fmt.Sprintf("gres/gpu=%s", quantity.String())
				}
			}
			if strings.HasPrefix(resourceName.String(), resourcev1.ResourceDeviceClassPrefix) {
				if quantity.Cmp(gresMax) > 0 {
					deviceClass := strings.TrimPrefix(string(resourceName), resourcev1.ResourceDeviceClassPrefix)
					gres = fmt.Sprintf("gres/gpu:%s=%s", deviceClass, quantity.String())
					gresMax = quantity
				}
			}
		}
	}
	if gres != "" {
		slurmJobIR.JobInfo.Gres = ptr.To(gres)
	}
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
		case wellknown.AnnotationGres:
			slurmJobIR.JobInfo.Gres = &value
		case wellknown.AnnotationGroupId:
			slurmJobIR.JobInfo.GroupId = &value
		case wellknown.AnnotationCpuPerTask:
			rs, err := resource.ParseQuantity(value)
			if err != nil {
				return err
			}
			val := int32(rs.Value()) //nolint:gosec // disable G115
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
			val /= 1048576 // value for 1024x1024 to follow what we need for slurm job IR
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
		case wellknown.AnnotationUserId:
			slurmJobIR.JobInfo.UserId = &value
		case wellknown.AnnotationWckey:
			slurmJobIR.JobInfo.Wckey = &value
		}
	}
	return nil
}
