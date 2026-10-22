// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

type registrationRecorder struct {
	record.EventRecorder
	events []corev1.Event
}

func (r *registrationRecorder) Eventf(obj runtime.Object, eventType, reason, format string, args ...interface{}) {
	r.events = append(r.events, corev1.Event{InvolvedObject: *obj.(*corev1.ObjectReference),
		Type: eventType, Reason: reason, Message: fmt.Sprintf(format, args...)})
}

type registrationSubmitFunc func(context.Context, *slurmjobir.SlurmJobIR) (int32, error)

func (f registrationSubmitFunc) RegisterJob(ctx context.Context, ir *slurmjobir.SlurmJobIR) (int32, error) {
	return f(ctx, ir)
}

func TestRegistrationEvents(t *testing.T) {
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
	ir := &slurmjobir.SlurmJobIR{RootPOM: metav1.PartialObjectMetadata{TypeMeta: group.TypeMeta, ObjectMeta: group.ObjectMeta},
		Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{*pod}}}}}
	recorder := &registrationRecorder{}
	failure := errors.New("Slurm rejected account")
	submissions := 0
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(group).Build(),
		schedulerName: "slurm-bridge", workloadAPI: api, recent: cache.NewExpiring(),
		eventRecorder: recorder, eventHistory: cache.NewExpiring(),
		submit: registrationSubmitFunc(func(context.Context, *slurmjobir.SlurmJobIR) (int32, error) {
			submissions++
			return 0, failure
		})}
	key := allocationKey(pod, ir.RootPOM)
	for range 2 {
		result, err := r.register(t.Context(), key, pod, ir, nil)
		if err != nil || result.RequeueAfter != registrationRetry {
			t.Fatalf("quorum wait changed: result=%v error=%v", result, err)
		}
	}
	if submissions != 0 || len(recorder.events) != 1 {
		t.Fatalf("quorum wait: submissions=%d events=%v", submissions, recorder.events)
	}
	if event := recorder.events[0]; event.Type != corev1.EventTypeNormal || event.Reason != "WaitingForMembers" || event.Message != slurmjobir.ErrorInsuffientPods.Error() || event.InvolvedObject.UID != group.UID {
		t.Fatalf("unexpected quorum event: %+v", event)
	}
	sibling := pod.DeepCopy()
	sibling.Name, sibling.UID = "sibling", "sibling-uid"
	ir.Components[0].Pods.Items = append(ir.Components[0].Pods.Items, *sibling)
	if _, err := r.register(t.Context(), key, pod, ir, nil); !errors.Is(err, failure) {
		t.Fatalf("submission error was lost: %v", err)
	}
	if submissions != 1 || len(recorder.events) != 2 {
		t.Fatalf("submission failure: submissions=%d events=%v", submissions, recorder.events)
	}
	if event := recorder.events[1]; event.Type != corev1.EventTypeWarning || event.Reason != "SlurmRegistrationFailed" || event.Message != failure.Error() || event.InvolvedObject.UID != group.UID {
		t.Fatalf("unexpected failure event: %+v", event)
	}
}
