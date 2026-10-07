// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeWorkloadAPIDiscovery struct {
	resources map[string]*metav1.APIResourceList
	errors    map[string]error
	calls     []string
}

func (f *fakeWorkloadAPIDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	f.calls = append(f.calls, groupVersion)
	if err := f.errors[groupVersion]; err != nil {
		return nil, err
	}
	return f.resources[groupVersion], nil
}

func workloadResources(groupVersion string) *metav1.APIResourceList {
	return &metav1.APIResourceList{
		GroupVersion: groupVersion,
		APIResources: []metav1.APIResource{{Name: "workloads"}, {Name: "podgroups"}},
	}
}

func missingWorkloadAPI(groupVersion string) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: workloadAPIGroup, Resource: "groupversions"}, groupVersion)
}

func TestRegisterWorkloadAPI(t *testing.T) {
	beta := workloadAPIGroup + "/" + WorkloadAPIVersionV1Beta1
	alpha := workloadAPIGroup + "/" + WorkloadAPIVersionV1Alpha2
	tests := []struct {
		name      string
		discovery *fakeWorkloadAPIDiscovery
		want      string
		wantCalls []string
		wantErr   bool
	}{
		{
			name: "prefer beta",
			discovery: &fakeWorkloadAPIDiscovery{resources: map[string]*metav1.APIResourceList{
				beta:  workloadResources(beta),
				alpha: workloadResources(alpha),
			}},
			want:      beta,
			wantCalls: []string{beta},
		},
		{
			name: "fall back to alpha",
			discovery: &fakeWorkloadAPIDiscovery{
				resources: map[string]*metav1.APIResourceList{alpha: workloadResources(alpha)},
				errors:    map[string]error{beta: missingWorkloadAPI(beta)},
			},
			want:      alpha,
			wantCalls: []string{beta, alpha},
		},
		{
			name: "neither version served",
			discovery: &fakeWorkloadAPIDiscovery{errors: map[string]error{
				beta:  missingWorkloadAPI(beta),
				alpha: missingWorkloadAPI(alpha),
			}},
			wantCalls: []string{beta, alpha},
			wantErr:   true,
		},
		{
			name: "discovery failure",
			discovery: &fakeWorkloadAPIDiscovery{errors: map[string]error{
				beta: errors.New("discovery unavailable"),
			}},
			wantCalls: []string{beta},
			wantErr:   true,
		},
		{
			name: "partial beta does not silently downgrade",
			discovery: &fakeWorkloadAPIDiscovery{resources: map[string]*metav1.APIResourceList{
				beta:  {GroupVersion: beta, APIResources: []metav1.APIResource{{Name: "podgroups"}}},
				alpha: workloadResources(alpha),
			}},
			wantCalls: []string{beta},
			wantErr:   true,
		},
		{
			name: "partial alpha",
			discovery: &fakeWorkloadAPIDiscovery{
				resources: map[string]*metav1.APIResourceList{
					alpha: {GroupVersion: alpha, APIResources: []metav1.APIResource{{Name: "workloads"}}},
				},
				errors: map[string]error{beta: missingWorkloadAPI(beta)},
			},
			wantCalls: []string{beta, alpha},
			wantErr:   true,
		},
		{
			name:      "empty discovery response",
			discovery: &fakeWorkloadAPIDiscovery{},
			wantCalls: []string{beta},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			api, err := RegisterWorkloadAPI(tt.discovery, scheme)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RegisterWorkloadAPI() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(tt.discovery.calls, tt.wantCalls) {
				t.Errorf("discovery calls = %v, want %v", tt.discovery.calls, tt.wantCalls)
			}
			got := ""
			if api != nil {
				got = api.PodGroupTypeMeta.APIVersion
			}
			if got != tt.want {
				t.Errorf("selected API = %q, want %q", got, tt.want)
			}
			for _, version := range []string{WorkloadAPIVersionV1Alpha2, WorkloadAPIVersionV1Beta1} {
				gvk := schema.GroupVersion{Group: workloadAPIGroup, Version: version}.WithKind("PodGroup")
				if got, want := scheme.Recognizes(gvk), tt.want == gvk.GroupVersion().String(); got != want {
					t.Errorf("scheme recognizes %s = %v, want %v", gvk, got, want)
				}
			}
		})
	}
}

func TestRegisteredWorkloadAPIDecodesBothVersions(t *testing.T) {
	tests := []struct {
		name             string
		version          string
		spec             string
		wantWorkloadName string
		wantCondition    string
	}{
		{
			name:             "alpha",
			version:          WorkloadAPIVersionV1Alpha2,
			spec:             `"podGroupTemplateRef":{"workload":{"workloadName":"training","podGroupTemplateName":"workers"}}`,
			wantWorkloadName: "training",
			wantCondition:    "PodGroupScheduled",
		},
		{
			name:             "beta",
			version:          WorkloadAPIVersionV1Beta1,
			spec:             `"workloadRef":{"workloadName":"training"}`,
			wantWorkloadName: "training",
			wantCondition:    "PodGroupInitiallyScheduled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			api := mustRegisterWorkloadAPI(t, scheme, tt.version)
			payload := []byte(`{"apiVersion":"` + api.PodGroupTypeMeta.APIVersion + `","kind":"PodGroup","metadata":{"name":"workers"},"spec":{` + tt.spec + `,"schedulingPolicy":{"gang":{"minCount":2}}}}`)
			obj, _, err := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode(payload, nil, nil)
			if err != nil {
				t.Fatalf("decode PodGroup: %v", err)
			}
			pg := obj.(*PodGroup)
			if pg.workloadName() != tt.wantWorkloadName {
				t.Errorf("workload name = %q, want %q", pg.workloadName(), tt.wantWorkloadName)
			}
			if pg.gangMinCount() == nil || *pg.gangMinCount() != 2 {
				t.Errorf("gang minCount = %v, want 2", pg.gangMinCount())
			}
			if api.ScheduledCondition != tt.wantCondition {
				t.Errorf("condition type = %q, want %q", api.ScheduledCondition, tt.wantCondition)
			}
		})
	}
}

func TestRegisteredWorkloadAPIEncodesGetOptions(t *testing.T) {
	for _, version := range []string{WorkloadAPIVersionV1Alpha2, WorkloadAPIVersionV1Beta1} {
		t.Run(version, func(t *testing.T) {
			scheme := runtime.NewScheme()
			mustRegisterWorkloadAPI(t, scheme, version)
			groupVersion := schema.GroupVersion{Group: workloadAPIGroup, Version: version}
			if _, err := runtime.NewParameterCodec(scheme).EncodeParameters(&metav1.GetOptions{}, groupVersion); err != nil {
				t.Fatalf("encode GetOptions for %s: %v", groupVersion, err)
			}
		})
	}
}

func TestListCompositePodGroup(t *testing.T) {
	inNamespace := newSchedulingV1Alpha3Object("CompositePodGroup", "matching", map[string]any{
		"parentCompositePodGroupName": "parent",
		"workloadRef":                 map[string]any{"templateName": "template", "workloadName": "workload"},
		"schedulingPolicy":            map[string]any{"gang": map[string]any{"minGroupCount": int64(2)}},
	})
	inNamespace.SetNamespace("target")
	otherNamespace := newSchedulingV1Alpha3Object("CompositePodGroup", "other", nil)
	reader := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(inNamespace, otherNamespace).Build()
	tr := &translator{Reader: reader, ctx: context.Background()}

	got, err := tr.listCompositePodGroup("target")
	if err != nil {
		t.Fatalf("listCompositePodGroup(target) error = %v, want nil", err)
	}
	minGroupCount := int64(2)
	want := []compositePodGroupInfo{{name: "matching", parentName: "parent", templateName: "template", workloadName: "workload", minGroupCount: &minGroupCount}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listCompositePodGroup(target) = %v, want %v", got, want)
	}
}

func TestValidateCompositePodGroup(t *testing.T) {
	tests := []struct {
		name      string
		spec      map[string]any
		wantField string
	}{
		{
			name: "supported fields",
			spec: map[string]any{
				"schedulingPolicy": map[string]any{"gang": map[string]any{"minGroupCount": int64(2)}},
			},
		},
		{
			name: "empty unsupported fields",
			spec: map[string]any{
				"priorityClassName": "",
				"schedulingConstraints": map[string]any{
					"topology": []any{},
				},
			},
		},
		{
			name: "topology key only",
			spec: map[string]any{
				"schedulingConstraints": map[string]any{
					"topology": []any{
						map[string]any{"key": "topology.kubernetes.io/zone"},
					},
				},
			},
			wantField: "spec.schedulingConstraints.topology",
		},
		{
			name:      "priority class",
			spec:      map[string]any{"priorityClassName": "high-priority"},
			wantField: "spec.priorityClassName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := newSchedulingV1Alpha3Object("CompositePodGroup", "test", tt.spec)
			err := validateCompositePodGroup(obj)
			if gotErr, wantErr := err != nil, tt.wantField != ""; gotErr != wantErr {
				t.Fatalf("validateCompositePodGroup(%v) error = %v, want error presence = %t", tt.spec, err, wantErr)
			}
			if err == nil {
				return
			}
			if !errors.Is(err, ErrorCompositePodGroupUnsupported) {
				t.Errorf("validateCompositePodGroup(%v) error = %v, want %v", tt.spec, err, ErrorCompositePodGroupUnsupported)
			}
			if !strings.Contains(err.Error(), tt.wantField) {
				t.Errorf("validateCompositePodGroup(%v) error = %q, want field %q", tt.spec, err, tt.wantField)
			}
		})
	}
}
