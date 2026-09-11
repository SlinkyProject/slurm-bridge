// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"fmt"

	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	WorkloadAPIVersionV1Alpha2 = "v1alpha2"
	WorkloadAPIVersionV1Beta1  = "v1beta1"

	workloadAPIGroup = "scheduling.k8s.io"
)

var (
	podGroupV1Alpha2 = metav1.TypeMeta{APIVersion: workloadAPIGroup + "/" + WorkloadAPIVersionV1Alpha2, Kind: "PodGroup"}
	podGroupV1Beta1  = metav1.TypeMeta{APIVersion: workloadAPIGroup + "/" + WorkloadAPIVersionV1Beta1, Kind: "PodGroup"}
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

// PodGroup is the common wire shape used by the v1alpha2 and v1beta1 APIs.
// Only the fields consumed by slurm-bridge need to be represented here.
type PodGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PodGroupSpec   `json:"spec,omitempty"`
	Status            PodGroupStatus `json:"status,omitempty"`
}

type PodGroupSpec struct {
	PodGroupTemplateRef *schedulingv1alpha2.PodGroupTemplateReference `json:"podGroupTemplateRef,omitempty"`
	WorkloadRef         *WorkloadReference                            `json:"workloadRef,omitempty"`
	SchedulingPolicy    schedulingv1alpha2.PodGroupSchedulingPolicy   `json:"schedulingPolicy,omitempty"`
}

type WorkloadReference struct {
	WorkloadName string `json:"workloadName"`
}

type PodGroupStatus = schedulingv1alpha2.PodGroupStatus

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
	if in.Spec.PodGroupTemplateRef != nil {
		out.Spec.PodGroupTemplateRef = in.Spec.PodGroupTemplateRef.DeepCopy()
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

// RegisterWorkloadAPI discovers and registers one built-in Workload API
// version. The beta version is preferred when both are advertised.
// A complete, supported Workload and PodGroup API is required.
func RegisterWorkloadAPI(discovery workloadAPIResourceDiscovery, scheme *runtime.Scheme) (*WorkloadAPI, error) {
	// TODO: Document the v1alpha2 cleanup and v1beta1 recreation steps required
	// when upgrading a cluster from Kubernetes 1.36 to 1.37.
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
	groupVersion := schema.GroupVersion{Group: workloadAPIGroup, Version: version}
	api := &WorkloadAPI{
		PodGroupTypeMeta: metav1.TypeMeta{APIVersion: groupVersion.String(), Kind: "PodGroup"},
	}
	switch version {
	case WorkloadAPIVersionV1Alpha2:
		api.ScheduledCondition = schedulingv1alpha2.PodGroupScheduled
	case WorkloadAPIVersionV1Beta1:
		api.ScheduledCondition = "PodGroupInitiallyScheduled"
	default:
		return nil, fmt.Errorf("unsupported Workload API version %q", version)
	}
	scheme.AddKnownTypeWithName(groupVersion.WithKind("PodGroup"), &PodGroup{})
	scheme.AddKnownTypeWithName(groupVersion.WithKind("Workload"), &Workload{})
	metav1.AddToGroupVersion(scheme, groupVersion)
	return api, nil
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
