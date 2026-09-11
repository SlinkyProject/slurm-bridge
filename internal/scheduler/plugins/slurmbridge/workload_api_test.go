// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestNewWorkloadAPI(t *testing.T) {
	for _, tt := range []struct {
		name      string
		disabled  bool
		version   string
		status    int
		wantCalls int
		wantErr   bool
	}{
		{name: "default requires APIs", status: http.StatusNotFound, wantCalls: 2, wantErr: true},
		{name: "default accepts beta", version: slurmjobir.WorkloadAPIVersionV1Beta1, wantCalls: 1},
		{name: "default accepts alpha", version: slurmjobir.WorkloadAPIVersionV1Alpha2, wantCalls: 2},
		{name: "enabled discovery failure is fatal", status: http.StatusForbidden, wantCalls: 1, wantErr: true},
		{name: "disabled skips unavailable APIs", disabled: true, status: http.StatusForbidden},
		{name: "disabled skips available APIs", disabled: true, version: slurmjobir.WorkloadAPIVersionV1Beta1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.disabled {
				featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.SlurmBridgeGenericWorkload, false)
			} else if !utilfeature.DefaultFeatureGate.Enabled(features.SlurmBridgeGenericWorkload) {
				t.Fatal("SlurmBridgeGenericWorkload must be enabled by default")
			}
			calls := 0
			config := &rest.Config{
				Host: "https://kubernetes.test",
				Transport: kubeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					status := tt.status
					var body any
					if tt.version != "" && req.URL.Path == "/apis/scheduling.k8s.io/"+tt.version {
						status = http.StatusOK
						body = &metav1.APIResourceList{
							GroupVersion: "scheduling.k8s.io/" + tt.version,
							APIResources: []metav1.APIResource{{Name: "workloads"}, {Name: "podgroups"}},
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
			api, err := newWorkloadAPI(config, scheme)
			if (err != nil) != tt.wantErr {
				t.Fatalf("newWorkloadAPI() error = %v, want error %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "SlurmBridgeGenericWorkload=true requires") {
				t.Errorf("startup error does not identify the required feature: %v", err)
			}
			if calls != tt.wantCalls {
				t.Errorf("discovery requests = %d, want %d", calls, tt.wantCalls)
			}
			for _, version := range []string{slurmjobir.WorkloadAPIVersionV1Beta1, slurmjobir.WorkloadAPIVersionV1Alpha2} {
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

func TestPreFilterRejectsDisabledPodGroupsBeforeSideEffects(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "worker", Labels: map[string]string{wellknown.LabelExternalJobId: "17"}},
		Spec:       corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}},
	}
	// No clients are supplied: rejection must happen before Kubernetes or Slurm
	// requests, including validation and cleanup of existing external job IDs.
	sb := &SlurmBridge{}
	state := framework.NewCycleState()
	result, status := sb.PreFilter(context.Background(), state, pod, nil)
	if result != nil || status.Code() != fwk.UnschedulableAndUnresolvable {
		t.Fatalf("PreFilter() = %v, %v; want an unresolvable scheduling rejection", result, status)
	}
	if message := status.Message(); !strings.Contains(message, "SlurmBridgeGenericWorkload=true") || !strings.Contains(message, "default/worker") {
		t.Fatalf("rejection must explain how to enable support: %s", message)
	}
	if _, err := state.Read(stateKey); err == nil {
		t.Fatal("ineligible Pod should not have scheduling state")
	}
	if _, err := slurmjobir.TranslateToSlurmJobIR(nil, nil, nil, context.Background(), pod); err == nil {
		t.Fatal("direct translation must also reject disabled PodGroups")
	}
}

func TestValidatePodGroupSupport(t *testing.T) {
	for _, tt := range []struct {
		name    string
		group   *corev1.PodSchedulingGroup
		api     *slurmjobir.WorkloadAPI
		wantErr bool
	}{
		{name: "ordinary Pod remains eligible"},
		{name: "empty group has no reference", group: &corev1.PodSchedulingGroup{}},
		{name: "empty name has no reference", group: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("")}},
		{name: "disabled native group", group: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}, wantErr: true},
		{name: "enabled native group", group: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}, api: &slurmjobir.WorkloadAPI{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{SchedulingGroup: tt.group}}
			if err := slurmjobir.ValidatePodGroupSupport(tt.api, pod); (err != nil) != tt.wantErr {
				t.Fatalf("eligibility error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}
