// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
)

type workloadAPIRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f workloadAPIRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewWorkloadAPI(t *testing.T) {
	for _, tt := range []struct {
		name      string
		disabled  bool
		version   string
		resources []metav1.APIResource
		status    int
		wantCalls int
		wantErr   bool
	}{
		{name: "default requires APIs", status: http.StatusNotFound, wantCalls: 2, wantErr: true},
		{name: "default accepts beta", version: WorkloadAPIVersionV1Beta1, wantCalls: 1},
		{name: "default accepts alpha", version: WorkloadAPIVersionV1Alpha2, wantCalls: 2},
		{name: "enabled discovery failure is fatal", status: http.StatusForbidden, wantCalls: 1, wantErr: true},
		{name: "transient beta failure does not fall back to alpha", version: WorkloadAPIVersionV1Alpha2, status: http.StatusServiceUnavailable, wantCalls: 1, wantErr: true},
		{name: "podgroups alone are insufficient", version: WorkloadAPIVersionV1Beta1, resources: []metav1.APIResource{{Name: "podgroups"}}, wantCalls: 1, wantErr: true},
		{name: "workloads alone are insufficient", version: WorkloadAPIVersionV1Beta1, resources: []metav1.APIResource{{Name: "workloads"}}, wantCalls: 1, wantErr: true},
		{name: "disabled skips unavailable APIs", disabled: true, status: http.StatusForbidden},
		{name: "disabled skips available APIs", disabled: true, version: WorkloadAPIVersionV1Beta1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.disabled {
				featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, false)
			} else if !utilfeature.DefaultFeatureGate.Enabled(features.GenericWorkload) {
				t.Fatal("GenericWorkload must be enabled by default")
			}
			calls := 0
			config := &rest.Config{
				Host: "https://kubernetes.test",
				Transport: workloadAPIRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					status := tt.status
					var body any
					if tt.version != "" && req.URL.Path == "/apis/scheduling.k8s.io/"+tt.version {
						status = http.StatusOK
						resources := tt.resources
						if resources == nil {
							resources = []metav1.APIResource{{Name: "workloads"}, {Name: "podgroups"}}
						}
						body = &metav1.APIResourceList{
							GroupVersion: "scheduling.k8s.io/" + tt.version,
							APIResources: resources,
						}
					} else {
						if status == 0 {
							status = http.StatusNotFound
						}
						body = &metav1.Status{Status: metav1.StatusFailure, Code: int32(status)} //nolint:gosec // HTTP status codes fit in int32.
					}
					data, err := json.Marshal(body)
					if err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {runtime.ContentTypeJSON}}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
				}),
			}
			scheme := runtime.NewScheme()
			api, err := NewWorkloadAPI(config, scheme)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewWorkloadAPI() error = %v, want error %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "GenericWorkload=true requires") {
				t.Errorf("startup error does not identify the required feature: %v", err)
			}
			if calls != tt.wantCalls {
				t.Errorf("discovery requests = %d, want %d", calls, tt.wantCalls)
			}
			for _, version := range []string{WorkloadAPIVersionV1Beta1, WorkloadAPIVersionV1Alpha2} {
				gvk := schema.GroupVersion{Group: "scheduling.k8s.io", Version: version}.WithKind("PodGroup")
				wantRegistered := !tt.disabled && !tt.wantErr && tt.version == version
				if scheme.Recognizes(gvk) != wantRegistered {
					t.Errorf("scheme registration for %s = %v, want %v", gvk, scheme.Recognizes(gvk), wantRegistered)
				}
			}
			if tt.disabled || tt.wantErr {
				if api != nil {
					t.Fatalf("unexpected Workload API: %#v", api)
				}
			} else if api == nil || api.PodGroupTypeMeta.APIVersion != "scheduling.k8s.io/"+tt.version {
				t.Fatalf("unexpected Workload API: %#v", api)
			}
		})
	}
}
