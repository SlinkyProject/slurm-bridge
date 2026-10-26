// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestClassifyRejection(t *testing.T) {
	slurm := func(n int32) error {
		return fmt.Errorf("create: %w", errors.Join(errors.New("Bad Request"), &slurmerrors.SlurmError{Number: n}))
	}
	for name, tt := range map[string]struct {
		err  error
		want rejection
	}{
		"invalid time limit":  {slurm(2051), rejectRequest},
		"invalid account":     {slurm(2045), rejectRequest},
		"invalid GRES string": {slurm(2072), rejectRequest},
		"invalid user ID":     {slurm(2165), rejectRequest},
		"accounting policy":   {slurm(2050), rejectLimit},
		"partition down":      {slurm(2101), rejectLimit},
		"update only code":    {slurm(2069), rejectOther},
		"no Slurm error":      {errors.New("connection refused"), rejectOther},
		"nil":                 {nil, rejectOther},
	} {
		if got := classifyRejection(tt.err); got != tt.want {
			t.Errorf("%s: got %v, want %v", name, got, tt.want)
		}
	}
}

func TestRegisterRejectionBehavior(t *testing.T) {
	for name, tt := range map[string]struct {
		number    int32
		wantRetry time.Duration
		wantType  string
		wantWhy   string
	}{
		"refused request retries slowly": {2072, requestRetry, corev1.EventTypeWarning, "SlurmRegistrationRejected"},
		"limit waits without errors":     {2050, limitRetry, corev1.EventTypeNormal, "WaitingForSlurm"},
	} {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			api, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
			if err != nil {
				t.Fatal(err)
			}
			group := &slurmjobir.PodGroup{TypeMeta: api.PodGroupTypeMeta,
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gang", UID: "group-uid"},
				Spec: slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
					Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}}}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: "pod-uid"},
				Spec: corev1.PodSpec{SchedulerName: "slurm-bridge"}}
			sibling := pod.DeepCopy()
			sibling.Name, sibling.UID = "sibling", "sibling-uid"
			ir := &slurmjobir.SlurmJobIR{RootPOM: metav1.PartialObjectMetadata{TypeMeta: group.TypeMeta, ObjectMeta: group.ObjectMeta},
				Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{*pod, *sibling}}}}}
			recorder := &registrationRecorder{}
			r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(group).Build(),
				schedulerName: "slurm-bridge", workloadAPI: api, recent: cache.NewExpiring(),
				eventRecorder: recorder, eventHistory: cache.NewExpiring(),
				submit: registrationSubmitFunc(func(context.Context, *slurmjobir.SlurmJobIR) (int32, error) {
					return 0, &slurmerrors.SlurmError{Number: tt.number, Message: "rejected"}
				})}
			result, err := r.register(t.Context(), allocationKey(pod, ir.RootPOM), pod, ir, nil)
			if err != nil {
				t.Fatalf("error must not trigger backoff: %v", err)
			}
			if result.RequeueAfter != tt.wantRetry {
				t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, tt.wantRetry)
			}
			if n := len(recorder.events); n == 0 || recorder.events[n-1].Type != tt.wantType || recorder.events[n-1].Reason != tt.wantWhy {
				t.Errorf("events = %+v", recorder.events)
			}
		})
	}
}
