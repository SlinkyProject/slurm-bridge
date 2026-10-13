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
						warnings, err = r.ValidateUpdate(ctx, pod.DeepCopy(), pod)
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
