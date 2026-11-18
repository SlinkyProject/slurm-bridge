// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/cache"
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

func TestRegistrationRejectsUnsupportedMembers(t *testing.T) {
	cases := []struct {
		name string
		spec corev1.PodSpec
	}{
		{"preferred node affinity", corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1}}}}}},
		{"required node affinity", corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{}}}}},
		{"pod affinity", corev1.PodSpec{Affinity: &corev1.Affinity{PodAffinity: &corev1.PodAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{Weight: 1}}}}}},
		{"pod anti-affinity", corev1.PodSpec{Affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{TopologyKey: "zone"}}}}}},
		{"topology spread", corev1.PodSpec{TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{WhenUnsatisfiable: corev1.ScheduleAnyway}}}},
		{"inline CSI", corev1.PodSpec{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "example.csi"}}}}}},
		{"generic ephemeral", corev1.PodSpec{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{}}}}}},
		{"host path", corev1.PodSpec{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/data"}}}}}},
		{"legacy volume", corev1.PodSpec{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{ISCSI: &corev1.ISCSIVolumeSource{}}}}}},
		{"node selector", corev1.PodSpec{NodeSelector: map[string]string{"rack": "a"}}},
	}
	for _, tc := range cases {
		for _, existing := range []bool{false, true} {
			for _, heterogeneous := range []bool{false, true} {
				name := tc.name + "/new"
				if existing {
					name = tc.name + "/existing"
				}
				if heterogeneous {
					name += "/heterogeneous"
				}
				t.Run(name, func(t *testing.T) {
					leader := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "ns", UID: "leader"},
						Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}}}
					worker := *leader.DeepCopy()
					worker.Name, worker.UID, worker.Spec = "worker", "worker", *tc.spec.DeepCopy()
					worker.Spec.SchedulerName = leader.Spec.SchedulerName
					worker.Spec.SchedulingGates = append(worker.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: wellknown.SchedulingGateRegistration})
					components := []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{leader, worker}}}}
					if heterogeneous {
						components = []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{leader}}}, {Pods: corev1.PodList{Items: []corev1.Pod{worker}}}}
					}
					ir := &slurmjobir.SlurmJobIR{Components: components}
					kube := fake.NewClientBuilder().WithObjects(&leader, &worker).Build()
					recorder := &registrationRecorder{}
					r := &Reconciler{Client: kube, schedulerName: leader.Spec.SchedulerName,
						recent: cache.NewExpiring(), eventRecorder: recorder, eventHistory: cache.NewExpiring(),
						submit: registrationSubmitFunc(func(context.Context, *slurmjobir.SlurmJobIR) (int32, error) {
							t.Fatal("unsupported workload reached Slurm submission")
							return 0, nil
						})}
					var jobs []slurmjob.RegisteredJob
					if existing {
						for i, component := range components {
							info := externaljobinfo.ExternalJobInfo{}
							for _, pod := range component.Pods.Items {
								info.Pods = append(info.Pods, externaljobinfo.PodKey(&pod))
							}
							job, err := slurmjob.RegisteredJobFromInfo(&api.V0044JobInfo{JobId: ptr.To(int32(100 + i)), AdminComment: ptr.To(info.ToString()), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}})
							if err != nil {
								t.Fatal(err)
							}
							jobs = append(jobs, job)
						}
					}
					key := podRequest(&leader)
					for range 2 {
						var err error
						if existing {
							_, err = r.publishRegistered(t.Context(), key, ir.AllPods(), jobs)
						} else {
							_, err = r.register(t.Context(), key, &leader, ir, jobs)
						}
						if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "ns/worker") {
							t.Fatalf("expected invalid sibling, got %v", err)
						}
					}
					if len(recorder.events) != 1 || recorder.events[0].Reason != "SlurmRegistrationFailed" || !strings.Contains(recorder.events[0].Message, "ns/worker") {
						t.Fatalf("missing or repeated rejection event: %v", recorder.events)
					}
					for _, original := range []*corev1.Pod{&leader, &worker} {
						actual := &corev1.Pod{}
						if err := kube.Get(t.Context(), client.ObjectKeyFromObject(original), actual); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(actual.Spec, original.Spec) || len(actual.Labels) != 0 || len(actual.Finalizers) != 0 {
							t.Fatalf("rejected allocation changed %s: %+v", actual.Name, actual)
						}
					}
				})
			}
		}
	}
}

func TestRegistrationPolicyIgnoresUnsupportedLateArrivals(t *testing.T) {
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

func TestRegistrationPolicyLifecycle(t *testing.T) {
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

func TestRegistrationPolicyPreservesGatedSibling(t *testing.T) {
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

func TestRegistrationPolicyPreservesDuplicateCleanup(t *testing.T) {
	w, err := newRegistrationWorld(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unsupported", Namespace: "world", UID: "unsupported"},
		Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway}},
			SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}},
			Containers:      []corev1.Container{{Name: "worker", Image: "busybox"}}}}
	if err := w.kube.Create(t.Context(), pod); err != nil {
		t.Fatal(err)
	}
	key := podRequest(pod)
	if _, err := w.reconciler.Reconcile(t.Context(), key); !apierrors.IsInvalid(err) {
		t.Fatalf("unsupported pod registered: %v", err)
	}
	if len(w.effects) != 0 {
		t.Fatalf("unsupported pod caused effects: %v", w.effects)
	}
	info := externaljobinfo.ExternalJobInfo{Pods: []string{externaljobinfo.PodKey(pod)}}
	for _, id := range []int32{100, 102} {
		state := api.V0044JobInfoJobStatePENDING
		if id == 102 {
			state = api.V0044JobInfoJobStateRUNNING
		}
		job := &api.V0044JobInfo{JobId: ptr.To(id), AdminComment: ptr.To(info.ToString()), JobState: &[]api.V0044JobInfoJobState{state}}
		if err := w.live.Create(t.Context(), job, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.advanceCache(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.reconciler.Reconcile(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	jobs, err := worldJobs(t.Context(), w.live)
	if err != nil || len(jobs) != 2 || worldUnfinished(jobs[0]) || !worldUnfinished(jobs[1]) {
		t.Fatalf("duplicate cleanup failed: %v, %v", jobs, err)
	}
	if err := w.advanceCache(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.reconciler.Reconcile(t.Context(), key); !apierrors.IsInvalid(err) {
		t.Fatalf("survivor bypassed policy: %v", err)
	}
	if len(w.effects) != 1 || w.effects[0].Operation != "slurm.cancel" || w.effects[0].JobID != 100 {
		t.Fatalf("unexpected effects: %v", w.effects)
	}
}
