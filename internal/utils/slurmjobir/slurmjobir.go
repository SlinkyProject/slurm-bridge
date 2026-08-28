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
	nvidiaDevicePlugin = "nvidia.com/gpu"
	amdDevicePlugin    = "amd.com/gpu"
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
	ctx                 context.Context
	draRegistry         *dra.Registry
	deviceClassProfiles map[string]dra.DeviceProfile
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

func PreFilter(c client.Client, registry *dra.Registry, ctx context.Context, pod *corev1.Pod, slurmJobIR *SlurmJobIR) *fwk.Status {
	t := translator{Reader: c, ctx: ctx, draRegistry: registry}
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

func TranslateToSlurmJobIR(c client.Client, registry *dra.Registry, ctx context.Context, pod *corev1.Pod) (slurmJobIR *SlurmJobIR, err error) {
	rootPOM, err := getRootOwnerMetadata(c, ctx, pod)
	if err != nil {
		return nil, err
	}

	t := translator{Reader: c, ctx: ctx, draRegistry: registry}

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
	if err := t.parseDeviceResources(slurmJobIR); err != nil {
		return nil, err
	}
	err = t.applySlurmAnnotations(slurmJobIR, pod, rootPOM)
	return slurmJobIR, err
}

/* Set CPU and Memory for the external job based on the maximum Pod CPU and Memory (including overhead) */
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

// parseDeviceResources resolves DRA extended resources to DeviceProfiles and
// dispatches their Slurm representation by backend. Core-bitmap quantities
// contribute to CPUs per task; indexed-GRES quantities contribute to GRES.
func (t *translator) parseDeviceResources(slurmJobIR *SlurmJobIR) error {
	maxByGRES := make(map[dra.GRES]resource.Quantity)
	for i := range slurmJobIR.Pods.Items {
		podGRES, coreBitmapCPU, err := t.podDeviceResources(&slurmJobIR.Pods.Items[i])
		if err != nil {
			return err
		}
		mergeMaxGRESQuantities(maxByGRES, podGRES)
		if coreBitmapCPU.Value() > 0 && (slurmJobIR.JobInfo.CpuPerTask == nil || coreBitmapCPU.Value() > int64(*slurmJobIR.JobInfo.CpuPerTask)) {
			slurmJobIR.JobInfo.CpuPerTask = ptr.To(int32(coreBitmapCPU.Value())) //nolint:gosec
		}
	}

	if gres := formatGRESResources(maxByGRES); gres != "" {
		slurmJobIR.JobInfo.Gres = ptr.To(gres)
	}
	return nil
}

func (t *translator) podDeviceResources(pod *corev1.Pod) (map[dra.GRES]resource.Quantity, resource.Quantity, error) {
	resources := make(map[dra.GRES]resource.Quantity)
	limits := resourcehelper.PodLimits(pod, resourcehelper.PodResourcesOptions{})
	requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	coreRequest, err := t.coreBitmapQuantity(requests)
	if err != nil {
		return nil, resource.Quantity{}, err
	}
	coreLimit, err := t.coreBitmapQuantity(limits)
	if err != nil {
		return nil, resource.Quantity{}, err
	}
	coreBitmapCPU := coreRequest
	if coreLimit.Cmp(coreBitmapCPU) > 0 {
		coreBitmapCPU = coreLimit
	}
	nativeCPU := *requests.Cpu()
	if limits.Cpu().Cmp(nativeCPU) > 0 {
		nativeCPU = *limits.Cpu()
	}
	if nativeCPU.Sign() > 0 && coreBitmapCPU.Sign() > 0 {
		return nil, resource.Quantity{}, fmt.Errorf("pod %s requests both native CPU and a core-bitmap DeviceProfile", pod.Name)
	}

	for resourceName, quantity := range limits {
		if quantity.Sign() <= 0 {
			continue
		}
		name := resourceName.String()
		// Explicit GPU extended resources remain device-plugin requests.
		// Only deviceclass.resource.kubernetes.io/<class> selects DRA and
		// dispatches through the resolved DeviceProfile backend.
		if resourceName == nvidiaDevicePlugin || resourceName == amdDevicePlugin {
			addGRESQuantity(resources, dra.GRES{Name: "gpu"}, quantity)
			continue
		}

		className, ok := strings.CutPrefix(name, resourcev1.ResourceDeviceClassPrefix)
		if !ok {
			continue
		}
		profile, err := t.resolveDeviceClass(className)
		if err != nil {
			return nil, resource.Quantity{}, err
		}
		if profile.UsesCoreBitmap() {
			continue
		}
		if !profile.UsesIndexedGRES() {
			return nil, resource.Quantity{}, fmt.Errorf("DeviceClass %q resolves to unsupported backend %q", className, profile.Backend.String())
		}
		gres, err := profile.GRES()
		if err != nil {
			return nil, resource.Quantity{}, err
		}
		addGRESQuantity(resources, gres, quantity)
	}
	return resources, coreBitmapCPU, nil
}

func (t *translator) coreBitmapQuantity(resources corev1.ResourceList) (resource.Quantity, error) {
	var total resource.Quantity
	for resourceName, quantity := range resources {
		if quantity.Sign() <= 0 {
			continue
		}
		className, ok := strings.CutPrefix(resourceName.String(), resourcev1.ResourceDeviceClassPrefix)
		if !ok {
			continue
		}
		profile, err := t.resolveDeviceClass(className)
		if err != nil {
			return resource.Quantity{}, err
		}
		if profile.UsesCoreBitmap() {
			total.Add(quantity)
		}
	}
	return total, nil
}

func (t *translator) resolveDeviceClass(className string) (dra.DeviceProfile, error) {
	if profile, ok := t.deviceClassProfiles[className]; ok {
		return profile, nil
	}
	if t.deviceClassProfiles == nil {
		t.deviceClassProfiles = make(map[string]dra.DeviceProfile)
	}

	deviceClass := &resourcev1.DeviceClass{}
	if err := t.Get(t.ctx, client.ObjectKey{Name: className}, deviceClass); err != nil {
		if apierrors.IsNotFound(err) {
			return dra.DeviceProfile{}, fmt.Errorf("DeviceClass %q was not found", className)
		}
		return dra.DeviceProfile{}, fmt.Errorf("get DeviceClass %q: %w", className, err)
	}

	profile, err := t.draRegistry.MatchDeviceClass(deviceClass)
	if err != nil {
		return dra.DeviceProfile{}, err
	}
	t.deviceClassProfiles[className] = profile
	return profile, nil
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
