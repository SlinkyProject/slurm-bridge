// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func TestLWSAdmissionPolicies(t *testing.T) {
	for _, tt := range []struct {
		name         string
		startup      lwsv1.StartupPolicyType
		restart      lwsv1.RestartPolicyType
		size         int32
		wantError    string
		wantWarnings int
	}{
		{name: "recreate group", startup: lwsv1.LeaderCreatedStartupPolicy, restart: lwsv1.RecreateGroupOnPodRestart, size: 2, wantWarnings: 1},
		{name: "unset restart policy uses the LWS default", size: 2, wantWarnings: 1},
		{name: "reject none", restart: lwsv1.NoneRestartPolicy, size: 2, wantError: "restartPolicy"},
		{name: "reject after start", restart: lwsv1.RecreateGroupAfterStart, size: 2, wantError: "restartPolicy"},
		{name: "reject leader ready", startup: lwsv1.LeaderReadyStartupPolicy, restart: lwsv1.RecreateGroupOnPodRestart, size: 2, wantError: "LeaderCreated"},
		{name: "single pod leader ready", startup: lwsv1.LeaderReadyStartupPolicy, restart: lwsv1.RecreateGroupOnPodRestart, size: 1, wantWarnings: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(corev1.AddToScheme(scheme))
			utilruntime.Must(lwsv1.AddToScheme(scheme))
			lws := &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{Name: "lws", Namespace: "default"},
				Spec: lwsv1.LeaderWorkerSetSpec{
					StartupPolicy:        tt.startup,
					LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{Size: ptr.To(tt.size), RestartPolicy: tt.restart},
				},
			}
			r := &PodAdmission{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(lws).Build(), SchedulerName: SchedulerName}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: lws.Namespace, Labels: map[string]string{lwsv1.SetNameLabelKey: lws.Name}},
				Spec:       corev1.PodSpec{SchedulerName: SchedulerName},
			}
			warnings, err := r.ValidateCreate(t.Context(), pod)
			if (err != nil) != (tt.wantError != "") {
				t.Fatalf("ValidateCreate(%s) error = %v, want error containing %q", tt.name, err, tt.wantError)
			}
			if tt.wantError != "" && !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("ValidateCreate(%s) error = %v, want it to contain %q", tt.name, err, tt.wantError)
			}
			if got := len(warnings); got != tt.wantWarnings {
				t.Errorf("ValidateCreate(%s) warnings = %v, want %d", tt.name, warnings, tt.wantWarnings)
			}
		})
	}
}
