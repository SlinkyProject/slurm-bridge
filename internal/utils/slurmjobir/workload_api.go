// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"fmt"

	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	WorkloadAPIVersionV1Alpha2 = "v1alpha2"
	WorkloadAPIVersionV1Beta1  = "v1beta1"

	workloadAPIGroup              = "scheduling.k8s.io"
	maxCompositePodGroupTreeDepth = 4
)

var (
	podGroupV1Alpha2          = metav1.TypeMeta{APIVersion: workloadAPIGroup + "/" + WorkloadAPIVersionV1Alpha2, Kind: "PodGroup"}
	podGroupV1Beta1           = metav1.TypeMeta{APIVersion: workloadAPIGroup + "/" + WorkloadAPIVersionV1Beta1, Kind: "PodGroup"}
	compositePodGroupV1Alpha3 = metav1.TypeMeta{APIVersion: workloadAPIGroup + "/v1alpha3", Kind: "CompositePodGroup"}
	// workloadV1Alpha3          = metav1.TypeMeta{APIVersion: workloadAPIGroup + "/v1alpha3", Kind: "Workload"}
)

type workloadAPIResourceDiscovery interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

// WorkloadAPI describes the built-in Kubernetes Workload and PodGroup API
// version selected for this scheduler process.
type WorkloadAPI struct {
	PodGroupTypeMeta   metav1.TypeMeta
	ScheduledCondition string
}

type compositePodGroupInfo struct {
	name          string
	parentName    string
	templateName  string
	workloadName  string
	minGroupCount *int64
}

// PodGroup is the common wire shape used by the v1alpha2 and v1beta1 APIs.
// Only the fields consumed by slurm-bridge need to be represented here.
type PodGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PodGroupSpec   `json:"spec,omitempty"`
	Status            PodGroupStatus `json:"status,omitempty"`
}

type PodGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodGroup `json:"items"`
}

type PodGroupSpec struct {
	ParentCompositePodGroupName *string                                    `json:"parentCompositePodGroupName,omitempty"`
	PodGroupTemplateRef         *PodGroupTemplateReference                 `json:"podGroupTemplateRef,omitempty"`
	WorkloadRef                 *WorkloadReference                         `json:"workloadRef,omitempty"`
	SchedulingPolicy            schedulingv1beta1.PodGroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`
	// Read only to reject what slurm-bridge cannot honor.
	SchedulingConstraints *schedulingv1beta1.PodGroupSchedulingConstraints `json:"schedulingConstraints,omitempty"`
	ResourceClaims        []schedulingv1beta1.PodGroupResourceClaim        `json:"resourceClaims,omitempty"`
}

// PodGroupTemplateReference retains the Kubernetes 1.36 wire format after
// the v1alpha2 Go package was removed in Kubernetes 1.37.
type PodGroupTemplateReference struct {
	Workload *WorkloadPodGroupTemplateReference `json:"workload,omitempty"`
}

type WorkloadPodGroupTemplateReference struct {
	WorkloadName         string `json:"workloadName"`
	PodGroupTemplateName string `json:"podGroupTemplateName"`
}

type WorkloadReference struct {
	TemplateName string `json:"templateName"`
	WorkloadName string `json:"workloadName"`
}

type PodGroupStatus = schedulingv1beta1.PodGroupStatus

// Workload carries the metadata used for Slurm annotations. The remainder of
// the Workload object is deliberately left to the API server.
type Workload = metav1.PartialObjectMetadata

func (in *PodGroup) DeepCopy() *PodGroup {
	if in == nil {
		return nil
	}
	out := new(PodGroup)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Spec.ParentCompositePodGroupName != nil {
		out.Spec.ParentCompositePodGroupName = new(string)
		*out.Spec.ParentCompositePodGroupName = *in.Spec.ParentCompositePodGroupName
	}
	if in.Spec.PodGroupTemplateRef != nil {
		ref := *in.Spec.PodGroupTemplateRef
		if ref.Workload != nil {
			workload := *ref.Workload
			ref.Workload = &workload
		}
		out.Spec.PodGroupTemplateRef = &ref
	}
	if in.Spec.WorkloadRef != nil {
		out.Spec.WorkloadRef = new(WorkloadReference)
		*out.Spec.WorkloadRef = *in.Spec.WorkloadRef
	}
	out.Spec.SchedulingPolicy = *in.Spec.SchedulingPolicy.DeepCopy()
	out.Status = *in.Status.DeepCopy()
	return out
}

func (in *PodGroup) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *PodGroupList) DeepCopy() *PodGroupList {
	if in == nil {
		return nil
	}

	out := new(PodGroupList)
	*out = *in
	in.DeepCopyInto(&out.ListMeta)

	if in.Items != nil {
		out.Items = make([]PodGroup, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopy()
		}
	}

	return out
}

func (in *PodGroupList) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

// RegisterWorkloadAPI discovers and registers one built-in Workload API
// version. The beta version is preferred when both are advertised.
// A complete, supported Workload and PodGroup API is required.
func RegisterWorkloadAPI(discovery workloadAPIResourceDiscovery, scheme *runtime.Scheme) (*WorkloadAPI, error) {
	for _, version := range []string{WorkloadAPIVersionV1Beta1, WorkloadAPIVersionV1Alpha2} {
		groupVersion := workloadAPIGroup + "/" + version
		resources, err := discovery.ServerResourcesForGroupVersion(groupVersion)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("discover Workload API %s: %w", groupVersion, err)
		}
		if !hasWorkloadAPIResources(resources) {
			return nil, fmt.Errorf("Workload API %s does not advertise workloads and podgroups", groupVersion)
		}
		return RegisterWorkloadAPIVersion(scheme, version)
	}
	return nil, fmt.Errorf("neither %s/%s nor %s/%s serves the Workload and PodGroup APIs", workloadAPIGroup, WorkloadAPIVersionV1Beta1, workloadAPIGroup, WorkloadAPIVersionV1Alpha2)
}

func hasWorkloadAPIResources(resources *metav1.APIResourceList) bool {
	if resources == nil {
		return false
	}
	foundWorkload := false
	foundPodGroup := false
	for _, resource := range resources.APIResources {
		switch resource.Name {
		case "workloads":
			foundWorkload = true
		case "podgroups":
			foundPodGroup = true
		}
	}
	return foundWorkload && foundPodGroup
}

// RegisterWorkloadAPIVersion maps the selected API version to the common wire
// types. Startup calls this once, so the client scheme contains only the API
// version served by its cluster.
func RegisterWorkloadAPIVersion(scheme *runtime.Scheme, version string) (*WorkloadAPI, error) {
	condition, err := ScheduledConditionForVersion(version)
	if err != nil {
		return nil, err
	}
	groupVersion := schema.GroupVersion{Group: workloadAPIGroup, Version: version}
	api := &WorkloadAPI{
		PodGroupTypeMeta:   metav1.TypeMeta{APIVersion: groupVersion.String(), Kind: "PodGroup"},
		ScheduledCondition: condition,
	}
	scheme.AddKnownTypeWithName(groupVersion.WithKind("PodGroup"), &PodGroup{})
	scheme.AddKnownTypeWithName(groupVersion.WithKind("PodGroupList"), &PodGroupList{})
	scheme.AddKnownTypeWithName(groupVersion.WithKind("Workload"), &Workload{})
	metav1.AddToGroupVersion(scheme, groupVersion)
	return api, nil
}

// ScheduledConditionForVersion returns the PodGroup scheduled condition type
// for a supported Workload API version.
func ScheduledConditionForVersion(version string) (string, error) {
	switch version {
	case WorkloadAPIVersionV1Alpha2:
		return "PodGroupScheduled", nil
	case WorkloadAPIVersionV1Beta1:
		return "PodGroupInitiallyScheduled", nil
	default:
		return "", fmt.Errorf("unsupported Workload API version %q", version)
	}
}

func isBuiltInPodGroup(typeMeta metav1.TypeMeta) bool {
	return typeMeta == podGroupV1Alpha2 || typeMeta == podGroupV1Beta1
}

func (pg *PodGroup) gangMinCount() *int32 {
	if pg.Spec.SchedulingPolicy.Gang == nil {
		return nil
	}
	return &pg.Spec.SchedulingPolicy.Gang.MinCount
}

func (pg *PodGroup) workloadName() string {
	if pg.Spec.WorkloadRef != nil {
		return pg.Spec.WorkloadRef.WorkloadName
	}
	if pg.Spec.PodGroupTemplateRef != nil && pg.Spec.PodGroupTemplateRef.Workload != nil {
		return pg.Spec.PodGroupTemplateRef.Workload.WorkloadName
	}
	return ""
}

func (t *translator) listCompositePodGroup(namespace string) ([]compositePodGroupInfo, error) {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion(compositePodGroupV1Alpha3.APIVersion)
	list.SetKind("CompositePodGroupList")
	if err := t.List(t.ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list CompositePodGroups in namespace %s: %w", namespace, err)
	}
	infos := make([]compositePodGroupInfo, 0, len(list.Items))
	for i := range list.Items {
		info, err := decodeCompositePodGroup(&list.Items[i])
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func (t *translator) getCompositePodGroup(key client.ObjectKey) (compositePodGroupInfo, error) {
	obj, err := t.getUnstructuredObject(compositePodGroupV1Alpha3, key)
	if err != nil {
		return compositePodGroupInfo{}, err
	}
	return decodeCompositePodGroup(obj)
}

func decodeCompositePodGroup(obj *unstructured.Unstructured) (compositePodGroupInfo, error) {
	key := client.ObjectKeyFromObject(obj)
	var err error
	info := compositePodGroupInfo{name: obj.GetName()}
	info.parentName, _, err = unstructured.NestedString(obj.Object, "spec", "parentCompositePodGroupName")
	if err != nil {
		return compositePodGroupInfo{}, fmt.Errorf("decode CompositePodGroup %s/%s parent name: %w", key.Namespace, key.Name, err)
	}
	info.templateName, _, err = unstructured.NestedString(obj.Object, "spec", "workloadRef", "templateName")
	if err != nil {
		return compositePodGroupInfo{}, fmt.Errorf("decode CompositePodGroup %s/%s template name: %w", key.Namespace, key.Name, err)
	}
	info.workloadName, _, err = unstructured.NestedString(obj.Object, "spec", "workloadRef", "workloadName")
	if err != nil {
		return compositePodGroupInfo{}, fmt.Errorf("decode CompositePodGroup %s/%s workload name: %w", key.Namespace, key.Name, err)
	}

	gang, found, err := unstructured.NestedMap(obj.Object, "spec", "schedulingPolicy", "gang")
	if err != nil {
		return compositePodGroupInfo{}, fmt.Errorf("decode CompositePodGroup %s/%s gang policy: %w", key.Namespace, key.Name, err)
	}
	if !found {
		return info, nil
	}

	minGroupCount, found, err := unstructured.NestedInt64(gang, "minGroupCount")
	if err != nil {
		return compositePodGroupInfo{}, fmt.Errorf("decode CompositePodGroup %s/%s minGroupCount: %w", key.Namespace, key.Name, err)
	}
	if !found {
		return compositePodGroupInfo{}, fmt.Errorf("decode CompositePodGroup %s/%s: gang policy has no minGroupCount", key.Namespace, key.Name)
	}
	info.minGroupCount = &minGroupCount
	return info, nil
}

func validateCompositePodGroup(obj *unstructured.Unstructured) error {
	if val, _, err := unstructured.NestedString(
		obj.Object, "spec", "priorityClassName",
	); err == nil && val != "" {
		return fmt.Errorf("%w: CompositePodGroup %s/%s sets unsupported field spec.priorityClassName",
			ErrorCompositePodGroupUnsupported, obj.GetNamespace(), obj.GetName())
	}

	if val, _, err := unstructured.NestedSlice(
		obj.Object, "spec", "schedulingConstraints", "topology",
	); err == nil && len(val) > 0 {
		return fmt.Errorf("%w: CompositePodGroup %s/%s sets unsupported field spec.schedulingConstraints.topology",
			ErrorCompositePodGroupUnsupported, obj.GetNamespace(), obj.GetName())
	}

	return nil
}

func (t *translator) compositePodGroupRootName(namespace, name string) (string, error) {
	for range maxCompositePodGroupTreeDepth {
		info, err := t.getCompositePodGroup(client.ObjectKey{Namespace: namespace, Name: name})
		if err != nil {
			return "", err
		}
		if info.parentName == "" {
			return name, nil
		}
		name = info.parentName
	}
	return "", fmt.Errorf("CompositePodGroup parent chain exceeds maximum depth of %d at %s/%s", maxCompositePodGroupTreeDepth, namespace, name)
}

// func (t *translator) getWireObject(typeMeta metav1.TypeMeta, key client.ObjectKey, out any) error {
// 	obj, err := t.getUnstructuredObject(typeMeta, key)
// 	if err != nil {
// 		return err
// 	}
// 	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, out); err != nil {
// 		return fmt.Errorf("decode %s %s/%s: %w", typeMeta.Kind, key.Namespace, key.Name, err)
// 	}
// 	return nil
// }

func (t *translator) getUnstructuredObject(typeMeta metav1.TypeMeta, key client.ObjectKey) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(typeMeta.APIVersion)
	obj.SetKind(typeMeta.Kind)
	if err := t.Get(t.ctx, key, obj); err != nil {
		return nil, fmt.Errorf("get %s %s/%s: %w", typeMeta.Kind, key.Namespace, key.Name, err)
	}
	return obj, nil
}
