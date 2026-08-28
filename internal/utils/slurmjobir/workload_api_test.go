// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
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

func TestTryRegisterWorkloadAPI(t *testing.T) {
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
		},
		{
			name: "discovery failure",
			discovery: &fakeWorkloadAPIDiscovery{errors: map[string]error{
				beta: errors.New("discovery unavailable"),
			}},
			wantCalls: []string{beta},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			api, err := TryRegisterWorkloadAPI(tt.discovery, scheme)
			if (err != nil) != tt.wantErr {
				t.Fatalf("TryRegisterWorkloadAPI() error = %v, wantErr %v", err, tt.wantErr)
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
