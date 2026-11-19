// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestPodAdmission_NodeNameMutation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		operation admissionv1.Operation
		namespace string
		scheduler string
		nodeName  string
		native    bool
		wantClear bool
		wantGate  bool
	}{
		{name: "managed default scheduler", operation: admissionv1.Create, namespace: namespace, scheduler: corev1.DefaultSchedulerName, nodeName: "worker", wantClear: true, wantGate: true},
		{name: "explicit bridge scheduler", operation: admissionv1.Create, namespace: "unmanaged", scheduler: SchedulerName, nodeName: "worker", wantClear: true, wantGate: true},
		{name: "native group", operation: admissionv1.Create, namespace: namespace, scheduler: SchedulerName, nodeName: "worker", native: true, wantClear: true},
		{name: "already unassigned", operation: admissionv1.Create, namespace: namespace, scheduler: SchedulerName, wantGate: true},
		{name: "bound update", operation: admissionv1.Update, namespace: namespace, scheduler: SchedulerName, nodeName: "worker"},
		{name: "other scheduler", operation: admissionv1.Create, namespace: namespace, scheduler: "other-scheduler", nodeName: "worker"},
		{name: "unmanaged default scheduler", operation: admissionv1.Create, namespace: "unmanaged", scheduler: corev1.DefaultSchedulerName, nodeName: "worker"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{SchedulerName: SchedulerName, ManagedNamespaces: []string{namespace}}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "preassigned", Namespace: tt.namespace},
				Spec: corev1.PodSpec{
					SchedulerName:   tt.scheduler,
					NodeName:        tt.nodeName,
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "example.com/hold"}},
				},
			}
			if tt.native {
				pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}
			}
			raw, err := json.Marshal(pod)
			if err != nil {
				t.Fatal(err)
			}
			req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: tt.operation,
				Object:    runtime.RawExtension{Raw: raw},
			}}
			webhook := r.mutatingWebhook(scheme.Scheme)
			response := webhook.Handle(context.Background(), req)
			if !response.Allowed {
				t.Fatalf("mutation denied: %v", response.Result)
			}
			removedNode := false
			for _, patch := range response.Patches {
				if patch.Path == "/spec/nodeName" {
					if patch.Operation != "remove" {
						t.Fatalf("unexpected nodeName patch: %+v", patch)
					}
					removedNode = true
				}
			}
			if removedNode != tt.wantClear {
				t.Errorf("nodeName removal = %v, want %v", removedNode, tt.wantClear)
			}
			if tt.wantClear {
				want := "spec.nodeName was removed so Slurm can select the node. Use a Slurm partition or constraint for placement."
				if !slices.Equal(response.Warnings, []string{want}) {
					t.Errorf("warnings = %v, want %q", response.Warnings, want)
				}
			} else if len(response.Warnings) != 0 {
				t.Errorf("unexpected warnings: %v", response.Warnings)
			}

			if err := r.Default(admission.NewContextWithRequest(context.Background(), req), pod); err != nil {
				t.Fatal(err)
			}
			wantNode := tt.nodeName
			if tt.wantClear {
				wantNode = ""
			}
			if pod.Spec.NodeName != wantNode {
				t.Errorf("nodeName = %q, want %q", pod.Spec.NodeName, wantNode)
			}
			wantGates := []corev1.PodSchedulingGate{{Name: "example.com/hold"}}
			if tt.wantGate {
				wantGates = append(wantGates, corev1.PodSchedulingGate{Name: wellknown.SchedulingGateRegistration})
			}
			if !slices.Equal(pod.Spec.SchedulingGates, wantGates) {
				t.Errorf("gates = %v, want %v", pod.Spec.SchedulingGates, wantGates)
			}
			raw, err = json.Marshal(pod)
			if err != nil {
				t.Fatal(err)
			}
			req.Object.Raw = raw
			response = webhook.Handle(context.Background(), req)
			if !response.Allowed || len(response.Patches) != 0 || len(response.Warnings) != 0 {
				t.Errorf("reinvocation should be unchanged without warnings: %+v", response)
			}
		})
	}
}
