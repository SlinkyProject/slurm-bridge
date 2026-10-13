// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
)

func TestPodAdmission_LegacyFeatureGates(t *testing.T) {
	for _, allowPodGroups := range []bool{true, false} {
		for _, allowLWS := range []bool{true, false} {
			t.Run(fmt.Sprintf("podgroups=%t/lws=%t", allowPodGroups, allowLWS), func(t *testing.T) {
				featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableLegacyv1alpha1PodGroups, allowPodGroups)
				featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableLegacyLWSGangs, allowLWS)
				for _, tt := range []struct {
					name            string
					labels          map[string]string
					schedulingGroup *corev1.PodSchedulingGroup
					unmanaged       bool
					legacyPodGroup  bool
					legacyLWS       bool
				}{
					{
						name: "legacy PodGroup", legacyPodGroup: true,
						labels: map[string]string{sched.PodGroupLabel: "legacy"},
					},
					{
						name: "legacy LWS", legacyLWS: true,
						labels: map[string]string{lwsv1.SetNameLabelKey: "lws"},
					},
					{
						name: "both legacy features", legacyPodGroup: true, legacyLWS: true,
						labels: map[string]string{sched.PodGroupLabel: "legacy", lwsv1.SetNameLabelKey: "lws"},
					},
					{
						name:            "native LWS",
						labels:          map[string]string{lwsv1.SetNameLabelKey: "lws"},
						schedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")},
					},
					{
						name: "native LWS with leftover legacy label", legacyPodGroup: true,
						labels:          map[string]string{sched.PodGroupLabel: "legacy", lwsv1.SetNameLabelKey: "lws"},
						schedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("native")},
					},
					{
						name: "unmanaged legacy Pod", unmanaged: true,
						labels: map[string]string{sched.PodGroupLabel: "legacy", lwsv1.SetNameLabelKey: "lws"},
					},
					{name: "ordinary Pod"},
				} {
					t.Run(tt.name, func(t *testing.T) {
						r := &PodAdmission{SchedulerName: SchedulerName}
						pod := &corev1.Pod{
							ObjectMeta: metav1.ObjectMeta{Namespace: "unmanaged", Labels: tt.labels},
							Spec:       corev1.PodSpec{SchedulerName: SchedulerName, SchedulingGroup: tt.schedulingGroup},
						}
						if tt.unmanaged {
							pod.Spec.SchedulerName = corev1.DefaultSchedulerName
						}
						for _, operation := range []string{"create", "update", "remove labels", "delete"} {
							t.Run(operation, func(t *testing.T) {
								var warnings admission.Warnings
								var err error
								ctx := contextWithAdmissionSubresource("")
								legacyPodGroup, legacyLWS := tt.legacyPodGroup, tt.legacyLWS
								switch operation {
								case "create":
									warnings, err = r.ValidateCreate(ctx, pod)
								case "update":
									warnings, err = r.ValidateUpdate(ctx, pod.DeepCopy(), pod)
								case "remove labels":
									newPod := pod.DeepCopy()
									newPod.Labels = nil
									warnings, err = r.ValidateUpdate(ctx, pod, newPod)
									legacyPodGroup, legacyLWS = false, false
								case "delete":
									warnings, err = r.ValidateDelete(ctx, pod)
									legacyPodGroup, legacyLWS = false, false
								}
								var rejected []string
								if legacyPodGroup && !allowPodGroups {
									rejected = append(rejected, "EnableLegacyv1alpha1PodGroups=false")
								}
								if legacyLWS && !allowLWS {
									rejected = append(rejected, "EnableLegacyLWSGangs=false")
								}
								if len(rejected) > 0 {
									if err == nil {
										t.Fatalf("expected rejection for %v", rejected)
									}
									for _, gate := range rejected {
										if !strings.Contains(err.Error(), gate) {
											t.Errorf("error %q does not identify %s", err, gate)
										}
									}
									if len(warnings) != 0 {
										t.Errorf("rejected Pod also returned warnings: %v", warnings)
									}
									return
								}
								if err != nil {
									t.Fatalf("unexpected rejection: %v", err)
								}
								wantWarnings := 0
								if legacyPodGroup {
									wantWarnings++
								}
								if legacyLWS {
									wantWarnings++
								}
								if len(warnings) != wantWarnings {
									t.Errorf("warnings = %v, want %d", warnings, wantWarnings)
								}
							})
						}
					})
				}
			})
		}
	}
}
