// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
)

type fakePodGroupDiscovery struct {
	resources     map[string]*metav1.APIResourceList
	discoveryErr  error
	serverVersion string
}

func (f fakePodGroupDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	if f.discoveryErr != nil {
		return nil, f.discoveryErr
	}
	if resources, found := f.resources[groupVersion]; found {
		return resources, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "groupversions"}, groupVersion)
}

func (f fakePodGroupDiscovery) ServerVersion() (*version.Info, error) {
	return &version.Info{GitVersion: f.serverVersion}, nil
}

func TestDiscoverKubernetesPodGroupAPI(t *testing.T) {
	t.Parallel()
	resources := &metav1.APIResourceList{
		APIResources: []metav1.APIResource{{Name: "workloads"}, {Name: "podgroups"}},
	}
	for _, tt := range []struct {
		name      string
		discovery fakePodGroupDiscovery
		want      kubernetesPodGroupAPI
		wantErr   bool
	}{
		{
			name: "Kubernetes 1.35 skips unsupported alpha1",
			discovery: fakePodGroupDiscovery{serverVersion: "v1.35.8", resources: map[string]*metav1.APIResourceList{
				"scheduling.k8s.io/v1alpha1": resources,
			}},
		},
		{
			name: "Kubernetes 1.36 uses alpha2",
			discovery: fakePodGroupDiscovery{serverVersion: "v1.36.4", resources: map[string]*metav1.APIResourceList{
				"scheduling.k8s.io/v1alpha2": resources,
			}},
			want: podGroupV1Alpha2,
		},
		{
			name: "Kubernetes 1.37 uses beta1",
			discovery: fakePodGroupDiscovery{serverVersion: "v1.37.0", resources: map[string]*metav1.APIResourceList{
				"scheduling.k8s.io/v1beta1": resources,
			}},
			want: podGroupV1Beta1,
		},
		{
			name: "both APIs prefer beta1",
			discovery: fakePodGroupDiscovery{resources: map[string]*metav1.APIResourceList{
				"scheduling.k8s.io/v1alpha2": resources,
				"scheduling.k8s.io/v1beta1":  resources,
			}},
			want: podGroupV1Beta1,
		},
		{
			name:      "missing API on 1.36 fails",
			discovery: fakePodGroupDiscovery{serverVersion: "v1.36.4"},
			wantErr:   true,
		},
		{
			name:      "missing API on 1.37 fails",
			discovery: fakePodGroupDiscovery{serverVersion: "v1.37.0"},
			wantErr:   true,
		},
		{
			name: "incomplete API fails",
			discovery: fakePodGroupDiscovery{resources: map[string]*metav1.APIResourceList{
				"scheduling.k8s.io/v1beta1": {APIResources: []metav1.APIResource{{Name: "podgroups"}}},
			}},
			wantErr: true,
		},
		{
			name: "discovery error does not fall back or skip",
			discovery: fakePodGroupDiscovery{serverVersion: "v1.35.8", discoveryErr: errors.New("discovery failed"),
				resources: map[string]*metav1.APIResourceList{"scheduling.k8s.io/v1alpha2": resources}},
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := discoverKubernetesPodGroupAPI(tt.discovery)
			if (err != nil) != tt.wantErr {
				t.Fatalf("discoverKubernetesPodGroupAPI() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("discoverKubernetesPodGroupAPI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKubernetesPodGroupScheduled(t *testing.T) {
	t.Parallel()
	for _, api := range []kubernetesPodGroupAPI{podGroupV1Alpha2, podGroupV1Beta1} {
		for _, tt := range []struct {
			condition string
			status    string
		}{
			{condition: "PodGroupScheduled", status: "True"},
			{condition: "PodGroupInitiallyScheduled", status: "True"},
			{condition: "PodGroupScheduled", status: "False"},
			{condition: "PodGroupInitiallyScheduled", status: "False"},
		} {
			t.Run(string(api)+"/"+tt.condition+"/"+tt.status, func(t *testing.T) {
				t.Parallel()
				podGroup := newKubernetesPodGroup(api, "workers", "training")
				podGroup.Object["status"] = map[string]any{"conditions": []any{map[string]any{
					"type": tt.condition, "status": tt.status,
				}}}
				want := tt.status == "True" && (api == podGroupV1Alpha2 && tt.condition == "PodGroupScheduled" ||
					api == podGroupV1Beta1 && tt.condition == "PodGroupInitiallyScheduled")
				got, err := kubernetesPodGroupScheduled(podGroup)
				if err != nil || got != want {
					t.Fatalf("kubernetesPodGroupScheduled() = %v, %v, want %v, nil", got, err, want)
				}
			})
		}
	}
}

func TestKubernetesPodGroupReferences(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		api          kubernetesPodGroupAPI
		reference    []string
		templateKey  string
		forbiddenKey string
	}{
		{podGroupV1Alpha2, []string{"spec", "podGroupTemplateRef", "workload"}, "podGroupTemplateName", "workloadRef"},
		{podGroupV1Beta1, []string{"spec", "workloadRef"}, "templateName", "podGroupTemplateRef"},
	} {
		t.Run(string(tt.api), func(t *testing.T) {
			t.Parallel()
			podGroup := newKubernetesPodGroup(tt.api, "workers", "training")
			reference, found, err := unstructured.NestedStringMap(podGroup.Object, tt.reference...)
			if err != nil || !found || reference["workloadName"] != "training" || reference[tt.templateKey] != "workers" {
				t.Fatalf("invalid Workload template reference: %v, found %v, error %v", reference, found, err)
			}
			if _, found, err := unstructured.NestedFieldNoCopy(podGroup.Object, "spec", tt.forbiddenKey); err != nil || found {
				t.Fatalf("PodGroup contains a reference field from the other API: found %v, error %v", found, err)
			}
		})
	}
}
