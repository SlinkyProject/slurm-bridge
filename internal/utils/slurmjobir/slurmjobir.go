// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	resourcehelper "k8s.io/component-helpers/resource"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const (
	nvidiaDevicePlugin            = "nvidia.com/gpu"
	amdDevicePlugin               = "amd.com/gpu"
	cpuDRADeviceClassExtendedName = resourcev1.ResourceDeviceClassPrefix + "dra.cpu"
)

type SlurmJobIRJobInfo struct {
	Account      *string
	CpuPerTask   *int32
	Constraints  *string
	Exclusive    *bool
	Gres         *string
	GroupId      *string
	JobName      *string
	Licenses     *string
	MemPerNode   *int64 // memory in megabytes
	MinNodes     *int32
	MaxNodes     *int32
	Nodes        []string
	ExcNodes     []string
	Partition    *string
	Priority     *int32
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

type workloadTranslator func(*translator, *corev1.Pod, *metav1.PartialObjectMetadata) (*SlurmJobIR, error)

func workloadTranslatorFor(typeMeta metav1.TypeMeta) (workloadTranslator, bool) {
	switch typeMeta {
	case podgroup_v1alpha2:
		return (*translator).fromPodGroup, true
	case jobSet_v1alpha2:
		return (*translator).fromJobSet, true
	case podgroup_coscheduling_v1alpha1:
		return (*translator).fromPodGroupCoscheduling, true
	case job_v1:
		return (*translator).fromJob, true
	case pod_v1:
		return func(t *translator, pod *corev1.Pod, _ *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
			return t.fromPod(pod)
		}, true
	case lws_v1:
		return (*translator).fromLws, true
	default:
		return nil, false
	}
}

func isSupportedWorkload(gvk schema.GroupVersionKind) bool {
	typeMeta := metav1.TypeMeta{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
	}
	_, ok := workloadTranslatorFor(typeMeta)
	return ok
}

func PreFilter(c client.Client, ctx context.Context, pod *corev1.Pod, slurmJobIR *SlurmJobIR) *fwk.Status {
	t := translator{Reader: c, ctx: ctx}
	switch slurmJobIR.RootPOM.TypeMeta {
	case podgroup_v1alpha2:
		return t.PreFilterPodGroup(pod, slurmJobIR)
	case podgroup_coscheduling_v1alpha1:
		return t.PreFilterPodGroupCoscheduling(pod, slurmJobIR)
	case lws_v1:
		return t.PreFilterLWS(pod, slurmJobIR)
	default:
		return fwk.NewStatus(fwk.Success)
	}
}

func TranslateToSlurmJobIR(c client.Client, ctx context.Context, pod *corev1.Pod) (slurmJobIR *SlurmJobIR, err error) {
	rootPOM, err := getRootOwnerMetadata(c, ctx, pod)
	if err != nil {
		return nil, err
	}

	t := translator{Reader: c, ctx: ctx}

	// PodGroup (scheduling.k8s.io/v1alpha2): pods opt in via spec.schedulingGroup.
	// Ref: https://kubernetes.io/docs/concepts/workloads/podgroup-api/
	if pgName, ok := podGroupName(pod); ok {
		rootPOM.TypeMeta = podgroup_v1alpha2
		rootPOM.Name = pgName
	} else if _, podGroup := t.GetPodGroupCoscheduling(pod); podGroup != nil {
		// PodGroup coscheduling does not conventionally own the Pod, rather is associated by the PodGroupLabel.
		// The Kubernetes co-scheduler would take the PodGroup into consideration when scheduling.
		rootPOM.TypeMeta = podgroup_coscheduling_v1alpha1
		rootPOM.Name = podGroup.Name
	}

	if err := t.Get(t.ctx, client.ObjectKeyFromObject(rootPOM), rootPOM); err != nil {
		return nil, err
	}

	translate, supported := workloadTranslatorFor(rootPOM.TypeMeta)
	if supported {
		slurmJobIR, err = translate(&t, pod, rootPOM)
	} else {
		slurmJobIR, err = t.fromPod(pod)
	}
	if err != nil {
		return nil, err
	}
	slurmJobIR.RootPOM = *rootPOM
	parsePodsCpuAndMemory(slurmJobIR)
	if err := t.parseGPUResources(slurmJobIR); err != nil {
		return nil, err
	}
	err = t.applySlurmAnnotations(slurmJobIR, pod, rootPOM)
	return slurmJobIR, err
}

/* Set CPU and Memory for the external job based on the maximum Pod CPU and Memory (including overhead) */
func parsePodsCpuAndMemory(slurmJobIR *SlurmJobIR) {
	var cpuMax resource.Quantity
	var memMax resource.Quantity
	cpuDRAResourceName := corev1.ResourceName(cpuDRADeviceClassExtendedName)
	for _, p := range slurmJobIR.Pods.Items {
		lim := resourcehelper.PodLimits(&p, resourcehelper.PodResourcesOptions{})
		req := resourcehelper.PodRequests(&p, resourcehelper.PodResourcesOptions{})
		if req.Cpu().Cmp(cpuMax) == 1 {
			cpuMax = *req.Cpu()
		}
		if lim.Cpu().Cmp(cpuMax) == 1 {
			cpuMax = *lim.Cpu()
		}
		if quantity := req[cpuDRAResourceName]; quantity.Cmp(cpuMax) == 1 {
			cpuMax = quantity
		}
		if quantity := lim[cpuDRAResourceName]; quantity.Cmp(cpuMax) == 1 {
			cpuMax = quantity
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

// parseGPUResources sets each GRES requirement to the maximum per-pod quantity
// requested across the external job. DeviceClasses which resolve to one
// DeviceProfile are combined because they consume the same Slurm GRES.
func (t *translator) parseGPUResources(slurmJobIR *SlurmJobIR) error {
	maxByGRES := make(map[dra.GRES]resource.Quantity)
	deviceClassCache := make(map[string]dra.GRES)
	for i := range slurmJobIR.Pods.Items {
		podGRES, err := t.podGPUResources(&slurmJobIR.Pods.Items[i], deviceClassCache)
		if err != nil {
			return err
		}
		mergeMaxGRESQuantities(maxByGRES, podGRES)
	}

	if gres := formatGRESResources(maxByGRES); gres != "" {
		slurmJobIR.JobInfo.Gres = ptr.To(gres)
	}
	return nil
}

func (t *translator) podGPUResources(pod *corev1.Pod, deviceClassCache map[string]dra.GRES) (map[dra.GRES]resource.Quantity, error) {
	resources := make(map[dra.GRES]resource.Quantity)
	limits := resourcehelper.PodLimits(pod, resourcehelper.PodResourcesOptions{})
	for resourceName, quantity := range limits {
		if quantity.Sign() <= 0 {
			continue
		}
		name := resourceName.String()
		if name == cpuDRADeviceClassExtendedName {
			continue
		}
		if resourceName == nvidiaDevicePlugin || resourceName == amdDevicePlugin {
			addGRESQuantity(resources, dra.GRES{Name: "gpu"}, quantity)
			continue
		}

		className, ok := strings.CutPrefix(name, resourcev1.ResourceDeviceClassPrefix)
		if !ok {
			continue
		}
		gres, ok := deviceClassCache[className]
		if !ok {
			var err error
			gres, err = t.deviceClassGRES(className)
			if err != nil {
				return nil, err
			}
			deviceClassCache[className] = gres
		}
		addGRESQuantity(resources, gres, quantity)
	}
	return resources, nil
}

func (t *translator) deviceClassGRES(className string) (dra.GRES, error) {
	// TODO: Replace this implicit fallback with explicit, versioned legacy
	// handling during upgrades. New jobs with a missing or non-matching
	// DeviceClass must fail closed; only jobs created by the legacy flow may use
	// a driver-named GRES.
	legacyGRES := dra.GRES{Name: "gpu", Type: className}
	deviceClass := &resourcev1.DeviceClass{}
	if err := t.Get(t.ctx, client.ObjectKey{Name: className}, deviceClass); err != nil {
		if apierrors.IsNotFound(err) {
			return legacyGRES, nil
		}
		return dra.GRES{}, fmt.Errorf("get DeviceClass %q: %w", className, err)
	}

	profile, err := dra.DefaultRegistry().MatchDeviceClass(deviceClass)
	if err != nil {
		return legacyGRES, nil //nolint:nilerr // Preserve the intentional legacy fallback for unmatched classes.
	}
	if len(deviceClass.Spec.Config) != 0 {
		return dra.GRES{}, fmt.Errorf("DeviceClass %q configuration is not supported", className)
	}
	return profile.GRES()
}

func addGRESQuantity(resources map[dra.GRES]resource.Quantity, gres dra.GRES, quantity resource.Quantity) {
	total := resources[gres]
	total.Add(quantity)
	resources[gres] = total
}

func mergeMaxGRESQuantities(maxByGRES, podGRES map[dra.GRES]resource.Quantity) {
	for gres, quantity := range podGRES {
		if current, ok := maxByGRES[gres]; !ok || quantity.Cmp(current) > 0 {
			maxByGRES[gres] = quantity
		}
	}
}

func formatGRESResources(resources map[dra.GRES]resource.Quantity) string {
	gresNames := make([]dra.GRES, 0, len(resources))
	for gres := range resources {
		gresNames = append(gresNames, gres)
	}
	slices.SortFunc(gresNames, func(a, b dra.GRES) int {
		if n := cmp.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return cmp.Compare(a.Type, b.Type)
	})
	entries := make([]string, len(gresNames))
	for i, gres := range gresNames {
		name := "gres/" + gres.Name
		if gres.Type != "" {
			name += ":" + gres.Type
		}
		quantity := resources[gres]
		entries[i] = name + "=" + quantity.String()
	}
	return strings.Join(entries, ",")
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
		case wellknown.AnnotationExclusive:
			v := strings.TrimSpace(strings.ToLower(value))
			exclusive := v != "false"
			slurmJobIR.JobInfo.Exclusive = &exclusive
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
		case wellknown.AnnotationPriority:
			num, err := ConvStrTo32(value)
			if err != nil {
				return err
			}
			slurmJobIR.JobInfo.Priority = num
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
