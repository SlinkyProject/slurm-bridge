// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Publication consumes allocation records; submission is covered against real
// Slurm by the round-trip e2e test. No Slurm client or registrar is installed here.
func TestPublishRegistration(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "first"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "second"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "bound"}},
	}
	builder := fake.NewClientBuilder()
	for i := range pods {
		pods[i].Spec.SchedulerName = "slurm-bridge"
		pods[i].Spec.SchedulingGates = []corev1.PodSchedulingGate{
			{Name: wellknown.SchedulingGateRegistration}, {Name: "other/gate"},
		}
		if i == 2 {
			pods[i].Spec.NodeName = "node"
		}
		builder.WithObjects(&pods[i])
	}
	failed := true
	patches := 0
	kube := builder.WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++
			if failed && obj.GetName() == "first" {
				return errors.New("patch conflict")
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	}).Build()
	r := &Reconciler{Client: kube, schedulerName: "slurm-bridge"}
	ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: pods}}}}
	components := []slurmjob.ExternalJob{{JobId: 107, HetJobId: 100, HetJobOffset: 1}}
	publish := func() error {
		t.Helper()
		for i := range pods {
			if err := kube.Get(t.Context(), client.ObjectKeyFromObject(&pods[i]), &ir.Components[0].Pods.Items[i]); err != nil {
				t.Fatal(err)
			}
		}
		return r.publish(t.Context(), ir, components)
	}
	check := func(name string, registered bool) {
		t.Helper()
		pod := &corev1.Pod{}
		if err := kube.Get(t.Context(), client.ObjectKey{Namespace: "ns", Name: name}, pod); err != nil {
			t.Fatal(err)
		}
		if hasRegistrationGate(pod) == registered {
			t.Fatalf("%s: registration gate=%t", name, hasRegistrationGate(pod))
		}
		if !slices.Contains(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: "other/gate"}) {
			t.Fatal("unrelated gate removed")
		}
		if registered && (pod.Labels[wellknown.LabelExternalJobId] != "107" || pod.Labels[wellknown.LabelExternalHetJobId] != "100" || pod.Labels[wellknown.LabelExternalHetJobOffset] != "1") {
			t.Fatalf("%s: wrong component metadata: %v", name, pod.Labels)
		}
	}
	if err := publish(); err == nil {
		t.Fatal("expected patch conflict")
	}
	check("first", false)
	check("second", true) // A failed member must not block the rest of the gang.
	check("bound", false)
	failed = false
	if err := publish(); err != nil {
		t.Fatal(err)
	}
	check("first", true)
	before := patches
	if err := publish(); err != nil {
		t.Fatal(err)
	}
	if patches != before {
		t.Fatal("unchanged registration patched again")
	}
}
