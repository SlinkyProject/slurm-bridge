// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestPodAdmission_LegacyFeatureGates(t *testing.T) {
	for _, tt := range []struct {
		name         string
		label        string
		disable      featuregate.Feature
		native       bool
		wantWarnings int
		wantError    bool
	}{
		{name: "LWS warns by default", label: lwsv1.SetNameLabelKey, wantWarnings: 1},
		{name: "reject legacy PodGroup", label: sched.PodGroupLabel,
			disable: features.EnableLegacyv1alpha1PodGroups, wantError: true},
		{name: "reject legacy LWS", label: lwsv1.SetNameLabelKey,
			disable: features.EnableLegacyLWSGangs, wantError: true},
		{name: "PodGroup gate leaves LWS warning", label: lwsv1.SetNameLabelKey,
			disable: features.EnableLegacyv1alpha1PodGroups, wantWarnings: 1},
		{name: "LWS gate leaves PodGroup warning", label: sched.PodGroupLabel,
			disable: features.EnableLegacyLWSGangs, wantWarnings: 1},
		{name: "native LWS remains allowed", label: lwsv1.SetNameLabelKey,
			disable: features.EnableLegacyLWSGangs, native: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.disable != "" {
				featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, tt.disable, false)
			}
			r := &PodAdmission{SchedulerName: SchedulerName}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{tt.label: "workload"}},
				Spec:       corev1.PodSpec{SchedulerName: SchedulerName},
			}
			if tt.native {
				pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")}
			}
			for _, operation := range []string{"create", "update"} {
				t.Run(operation, func(t *testing.T) {
					var warnings admission.Warnings
					var err error
					ctx := contextWithAdmissionSubresource("")
					if operation == "create" {
						warnings, err = r.ValidateCreate(ctx, pod)
					} else {
						oldPod := pod.DeepCopy()
						delete(oldPod.Labels, tt.label)
						warnings, err = r.ValidateUpdate(ctx, oldPod, pod)
					}
					if (err != nil) != tt.wantError {
						t.Fatalf("error = %v, wantError %t", err, tt.wantError)
					}
					if err != nil && !strings.Contains(err.Error(), string(tt.disable)+"=false") {
						t.Errorf("error %q does not identify disabled gate %s", err, tt.disable)
					}
					if len(warnings) != tt.wantWarnings {
						t.Errorf("warnings = %v, want %d", warnings, tt.wantWarnings)
					}
				})
			}
		})
	}
}

func TestPodAdmission_LegacyFeatureGateUpdates(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableLegacyv1alpha1PodGroups, false)
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableLegacyLWSGangs, false)
	r := &PodAdmission{SchedulerName: SchedulerName}
	ctx := contextWithAdmissionSubresource("")

	for _, legacy := range []struct {
		name       string
		label      string
		otherLabel string
		otherGate  featuregate.Feature
	}{
		{"PodGroup", sched.PodGroupLabel, lwsv1.SetNameLabelKey, features.EnableLegacyLWSGangs},
		{"LWS", lwsv1.SetNameLabelKey, sched.PodGroupLabel, features.EnableLegacyv1alpha1PodGroups},
	} {
		t.Run(legacy.name, func(t *testing.T) {
			for _, tt := range []struct {
				name         string
				update       func(*corev1.Pod)
				wantWarnings int
				wantError    bool
			}{
				{name: "remove finalizer", wantWarnings: 1, update: func(pod *corev1.Pod) {
					pod.Finalizers = nil
				}},
				{name: "patch job metadata", wantWarnings: 1, update: func(pod *corev1.Pod) {
					pod.Labels[wellknown.LabelExternalJobId] = "42"
					pod.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node1"}
				}},
				{name: "remove legacy usage", update: func(pod *corev1.Pod) {
					delete(pod.Labels, legacy.label)
				}},
				{name: "introduce other disabled feature", wantError: true, update: func(pod *corev1.Pod) {
					pod.Labels[legacy.otherLabel] = "other"
				}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					oldPod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Labels:     map[string]string{legacy.label: "workload"},
							Finalizers: []string{wellknown.FinalizerScheduler},
						},
						Spec: corev1.PodSpec{SchedulerName: SchedulerName},
					}
					newPod := oldPod.DeepCopy()
					tt.update(newPod)
					warnings, err := r.ValidateUpdate(ctx, oldPod, newPod)
					if (err != nil) != tt.wantError {
						t.Fatalf("error = %v, wantError %t", err, tt.wantError)
					}
					if err != nil && !strings.Contains(err.Error(), string(legacy.otherGate)+"=false") {
						t.Errorf("error %q does not identify newly introduced feature %s", err, legacy.otherGate)
					}
					if len(warnings) != tt.wantWarnings {
						t.Errorf("warnings = %v, want %d", warnings, tt.wantWarnings)
					}
				})
			}
		})
	}

	t.Run("native LWS does not exempt implicit LWS scheduling", func(t *testing.T) {
		oldPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{lwsv1.SetNameLabelKey: "workload"}},
			Spec: corev1.PodSpec{
				SchedulerName:   SchedulerName,
				SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")},
			},
		}
		newPod := oldPod.DeepCopy()
		newPod.Spec.SchedulingGroup = nil
		_, err := r.ValidateUpdate(ctx, oldPod, newPod)
		if err == nil || !strings.Contains(err.Error(), string(features.EnableLegacyLWSGangs)+"=false") {
			t.Fatalf("error = %v, want rejection of newly introduced legacy LWS", err)
		}
	})
}
