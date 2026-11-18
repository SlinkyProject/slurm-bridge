// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"reflect"
	"slices"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "github.com/SlinkyProject/slurm-client/api/v0044"

	bridgeadmission "github.com/SlinkyProject/slurm-bridge/internal/admission"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestRegistrationPublishesOnlyRecordedMembers(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "registered", Namespace: "ns", UID: "registered"},
		Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}}}
	late := pod.DeepCopy()
	late.Name, late.UID = "late", "late"
	late.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway}}
	kube := fake.NewClientBuilder().WithObjects(pod, late).Build()
	r := &Reconciler{Client: kube, schedulerName: pod.Spec.SchedulerName}
	info := externaljobinfo.ExternalJobInfo{Pods: []string{externaljobinfo.PodKey(pod)}}
	job, err := slurmjob.RegisteredJobFromInfo(&api.V0044JobInfo{JobId: ptr.To(int32(100)), AdminComment: ptr.To(info.ToString()), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.publishRegistered(t.Context(), podRequest(pod), []corev1.Pod{*pod, *late}, []slurmjob.RegisteredJob{job})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("publication should succeed while the late arrival waits: %v, %v", result, err)
	}
	for _, original := range []*corev1.Pod{pod, late} {
		actual := &corev1.Pod{}
		if err := kube.Get(t.Context(), client.ObjectKeyFromObject(original), actual); err != nil {
			t.Fatal(err)
		}
		if original == pod {
			if hasRegistrationGate(actual) || actual.Labels[wellknown.LabelExternalJobId] != "100" {
				t.Fatalf("registered member was not published: %+v", actual)
			}
		} else if !reflect.DeepEqual(actual.Spec, original.Spec) || len(actual.Labels) != 0 || len(actual.Finalizers) != 0 {
			t.Fatalf("late arrival was changed: %+v", actual)
		}
	}
}

func TestRegistrationAdmissionLifecycle(t *testing.T) {
	w, err := newRegistrationWorld(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "world", UID: "pod"},
		Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", Containers: []corev1.Container{{Name: "worker", Image: "busybox"}}}}
	webhook := &bridgeadmission.PodAdmission{SchedulerName: "slurm-bridge"}
	create := admission.NewContextWithRequest(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Create}})
	if err := webhook.Default(create, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := webhook.ValidateCreate(create, pod); err != nil {
		t.Fatal(err)
	}
	if !hasRegistrationGate(pod) {
		t.Fatal("admission did not gate registration")
	}
	if err := w.kube.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	key := podRequest(pod)
	for range 2 {
		if _, err := w.reconciler.Reconcile(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		if err := w.advanceCache(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	actual := &corev1.Pod{}
	if err := w.kube.Get(t.Context(), client.ObjectKeyFromObject(pod), actual); err != nil {
		t.Fatal(err)
	}
	if hasRegistrationGate(actual) || actual.Labels[wellknown.LabelExternalJobId] != "100" {
		t.Fatalf("registration did not publish: %+v", actual)
	}
	update := admission.NewContextWithRequest(t.Context(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update}})
	if _, err := webhook.ValidateUpdate(update, pod, actual); err != nil {
		t.Fatalf("registration publication rejected: %v", err)
	}
	jobs, err := worldJobs(t.Context(), w.live)
	if err != nil || len(jobs) != 1 || worldRank(jobs[0]) != 0 {
		t.Fatalf("expected one held allocation, got %v, %v", jobs, err)
	}
}

func TestRegistrationPublishesSiblingWithNodeSelector(t *testing.T) {
	w, err := newRegistrationWorld(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	group := &slurmjobir.PodGroup{TypeMeta: w.workloadAPI.PodGroupTypeMeta,
		ObjectMeta: metav1.ObjectMeta{Name: "gang", Namespace: "world", UID: "gang"},
		Spec: slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
			Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2}}}}
	key := objectRequest(group)
	if err := w.kube.Create(t.Context(), group); err != nil {
		t.Fatal(err)
	}
	leader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "world", UID: "leader"},
		Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(group.Name)},
			SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}},
			Containers:      []corev1.Container{{Name: "worker", Image: "busybox"}}}}
	worker := leader.DeepCopy()
	worker.Name, worker.UID = "worker", "worker"
	worker.Spec.NodeSelector = map[string]string{"pool": "batch"}
	worker.Spec.SchedulingGates = append(worker.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: "example.com/ready"})
	for _, pod := range []*corev1.Pod{leader, worker} {
		if err := w.kube.Create(t.Context(), pod); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := w.reconciler.Reconcile(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		if err := w.advanceCache(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, pod := range []*corev1.Pod{leader, worker} {
		actual := &corev1.Pod{}
		if err := w.kube.Get(t.Context(), client.ObjectKeyFromObject(pod), actual); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(actual.Spec.SchedulingGates, pod.Spec.SchedulingGates[1:]) || actual.Labels[wellknown.LabelExternalJobId] != "100" {
			t.Fatalf("registration did not preserve unrelated gates: %+v", actual)
		}
	}
	jobs, err := worldJobs(t.Context(), w.live)
	if err != nil || len(jobs) != 1 || worldRank(jobs[0]) != 0 {
		t.Fatalf("expected one held allocation, got %v, %v", jobs, err)
	}
}
