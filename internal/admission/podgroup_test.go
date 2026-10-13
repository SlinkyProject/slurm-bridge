// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

func TestPodAdmission_WorkloadDeprecation(t *testing.T) {
	for _, tt := range []struct {
		name              string
		namespace         string
		scheduler         string
		labels            map[string]string
		schedulingGroup   *corev1.PodSchedulingGroup
		namespaceSelector *metav1.LabelSelector
		wantWarnings      []string
	}{
		{
			name:         "legacy group in managed namespace",
			namespace:    namespace,
			scheduler:    corev1.DefaultSchedulerName,
			labels:       map[string]string{sched.PodGroupLabel: "legacy"},
			wantWarnings: []string{"scheduling.x-k8s.io/v1alpha1"},
		},
		{
			name:         "legacy group explicitly selects scheduler",
			namespace:    "unmanaged",
			scheduler:    SchedulerName,
			labels:       map[string]string{sched.PodGroupLabel: "legacy"},
			wantWarnings: []string{"scheduling.x-k8s.io/v1alpha1"},
		},
		{
			name:              "legacy group in selected namespace",
			namespace:         "selected",
			scheduler:         corev1.DefaultSchedulerName,
			labels:            map[string]string{sched.PodGroupLabel: "legacy"},
			namespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"managed": "true"}},
			wantWarnings:      []string{"scheduling.x-k8s.io/v1alpha1"},
		},
		{
			name:      "unmanaged legacy group",
			namespace: "unmanaged",
			scheduler: corev1.DefaultSchedulerName,
			labels:    map[string]string{sched.PodGroupLabel: "legacy"},
		},
		{
			name:      "empty legacy label",
			namespace: namespace,
			labels:    map[string]string{sched.PodGroupLabel: ""},
		},
		{
			name:      "no pod group",
			namespace: namespace,
		},
		{
			name:            "built-in group",
			namespace:       namespace,
			schedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")},
		},
		{
			name:            "built-in group with leftover legacy label",
			namespace:       namespace,
			labels:          map[string]string{sched.PodGroupLabel: "legacy"},
			schedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")},
			wantWarnings:    []string{"scheduling.x-k8s.io/v1alpha1"},
		},
		{
			name:         "legacy LWS in managed namespace",
			namespace:    namespace,
			scheduler:    corev1.DefaultSchedulerName,
			labels:       map[string]string{lwsv1.SetNameLabelKey: "lws"},
			wantWarnings: []string{"Implicit LeaderWorkerSet gang scheduling"},
		},
		{
			name:         "legacy LWS explicitly selects scheduler",
			namespace:    "unmanaged",
			scheduler:    SchedulerName,
			labels:       map[string]string{lwsv1.SetNameLabelKey: "lws"},
			wantWarnings: []string{"Implicit LeaderWorkerSet gang scheduling"},
		},
		{
			name:              "legacy LWS in selected namespace",
			namespace:         "selected",
			scheduler:         corev1.DefaultSchedulerName,
			labels:            map[string]string{lwsv1.SetNameLabelKey: "lws"},
			namespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"managed": "true"}},
			wantWarnings:      []string{"Implicit LeaderWorkerSet gang scheduling"},
		},
		{
			name:      "unmanaged LWS",
			namespace: "unmanaged",
			scheduler: corev1.DefaultSchedulerName,
			labels:    map[string]string{lwsv1.SetNameLabelKey: "lws"},
		},
		{
			name:      "empty LWS label",
			namespace: namespace,
			labels:    map[string]string{lwsv1.SetNameLabelKey: ""},
		},
		{
			name:            "LWS with native pod group",
			namespace:       namespace,
			labels:          map[string]string{lwsv1.SetNameLabelKey: "lws"},
			schedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")},
		},
		{
			name:            "LWS with missing pod group name",
			namespace:       namespace,
			labels:          map[string]string{lwsv1.SetNameLabelKey: "lws"},
			schedulingGroup: &corev1.PodSchedulingGroup{},
			wantWarnings:    []string{"Implicit LeaderWorkerSet gang scheduling"},
		},
		{
			name:            "LWS with empty pod group name",
			namespace:       namespace,
			labels:          map[string]string{lwsv1.SetNameLabelKey: "lws"},
			schedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("")},
			wantWarnings:    []string{"Implicit LeaderWorkerSet gang scheduling"},
		},
		{
			name:      "LWS with legacy pod group",
			namespace: namespace,
			labels: map[string]string{
				lwsv1.SetNameLabelKey: "lws",
				sched.PodGroupLabel:   "legacy",
			},
			wantWarnings: []string{"scheduling.x-k8s.io/v1alpha1", "Implicit LeaderWorkerSet gang scheduling"},
		},
		{
			name:      "LWS with native pod group and leftover legacy label",
			namespace: namespace,
			labels: map[string]string{
				lwsv1.SetNameLabelKey: "lws",
				sched.PodGroupLabel:   "legacy",
			},
			schedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")},
			wantWarnings:    []string{"scheduling.x-k8s.io/v1alpha1"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{
				Client: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: "selected", Labels: map[string]string{"managed": "true"}},
				}).Build(),
				SchedulerName:            SchedulerName,
				ManagedNamespaces:        []string{namespace},
				ManagedNamespaceSelector: tt.namespaceSelector,
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: tt.namespace, Labels: tt.labels},
				Spec:       corev1.PodSpec{SchedulerName: tt.scheduler, SchedulingGroup: tt.schedulingGroup},
			}
			for _, operation := range []string{"create", "update", "add label", "remove label", "delete"} {
				t.Run(operation, func(t *testing.T) {
					var warnings admission.Warnings
					var err error
					wantWarnings := tt.wantWarnings
					ctx := contextWithAdmissionSubresource("")
					switch operation {
					case "create":
						warnings, err = r.ValidateCreate(context.Background(), pod)
					case "update":
						warnings, err = r.ValidateUpdate(ctx, pod.DeepCopy(), pod)
					case "add label":
						oldPod := pod.DeepCopy()
						oldPod.Labels = nil
						warnings, err = r.ValidateUpdate(ctx, oldPod, pod)
					case "remove label":
						newPod := pod.DeepCopy()
						newPod.Labels = nil
						warnings, err = r.ValidateUpdate(ctx, pod, newPod)
						wantWarnings = nil
					case "delete":
						warnings, err = r.ValidateDelete(context.Background(), pod)
						wantWarnings = nil
					}
					if err != nil {
						t.Fatalf("admission rejected Pod: %v", err)
					}
					if len(warnings) != len(wantWarnings) {
						t.Fatalf("warnings = %v, want %v", warnings, wantWarnings)
					}
					for i, want := range wantWarnings {
						for _, part := range []string{
							want, "deprecated", "removed in a future release", "slurm-bridge",
							"Kubernetes native PodGroups", "spec.schedulingGroup.podGroupName",
						} {
							if !strings.Contains(warnings[i], part) {
								t.Errorf("warning %q does not contain %q", warnings[i], part)
							}
						}
					}
				})
			}
		})
	}
}
