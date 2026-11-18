// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/schedulingpolicy"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestSchedulingPolicyBlocksAllocation(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "preferred"}, Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1}}}}}}
	sb := &SlurmBridge{} // Any Kubernetes or Slurm call will panic.
	state := framework.NewCycleState()
	if _, status := sb.PreFilter(context.Background(), state, pod, nil); status.Code() != fwk.UnschedulableAndUnresolvable {
		t.Fatal(status)
	}
	if _, status := sb.PostFilter(context.Background(), state, pod, nil); status.Code() != fwk.UnschedulableAndUnresolvable {
		t.Fatal(status)
	}
	// A supported initiating pod cannot submit an unsupported sibling, including
	// when PostFilter is called with an IR already present in cycle state.
	ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{*pod}}}}}
	state.Write(stateKey, &stateData{slurmJobIR: ir})
	if _, status := sb.PostFilter(context.Background(), state, &corev1.Pod{}, nil); status.Code() != fwk.UnschedulableAndUnresolvable || !strings.Contains(status.Message(), "preferred") {
		t.Fatal(status)
	}
}

func TestSchedulingPolicyComponentBoundary(t *testing.T) {
	first := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader"}, Spec: corev1.PodSpec{NodeSelector: map[string]string{"rack": "a"}}}
	second := *first.DeepCopy()
	second.Name = "worker"
	second.Spec.NodeSelector["rack"] = "b"
	ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{first, second}}}}}
	state := framework.NewCycleState()
	state.Write(stateKey, &stateData{slurmJobIR: ir})
	sb := &SlurmBridge{}
	if _, status := sb.PostFilter(context.Background(), state, &first, nil); status.Code() != fwk.UnschedulableAndUnresolvable {
		t.Fatal(status)
	}
	ir.Components = []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{first}}}, {Pods: corev1.PodList{Items: []corev1.Pod{second}}}}
	for _, component := range ir.Components {
		if err := schedulingpolicy.ValidateComponent(component.Pods.Items); err != nil {
			t.Fatalf("different heterogeneous components rejected: %v", err)
		}
	}
}

func TestSchedulingPolicyRejectsGroupBeforePlacement(t *testing.T) {
	scheme, err := newClientScheme()
	if err != nil {
		t.Fatal(err)
	}
	leader := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "default", Labels: map[string]string{sched.PodGroupLabel: "group"}}}
	worker := leader.DeepCopy()
	worker.Name = "worker"
	worker.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway}}
	group := &sched.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: "group", Namespace: "default"}, Spec: sched.PodGroupSpec{MinMember: 2}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(leader, worker, group).Build()
	sb := &SlurmBridge{Client: kubeClient, slurmControl: &postFilterSlurmControl{}}
	state := framework.NewCycleState()
	_, status := sb.PreFilter(context.Background(), state, leader, nil)
	if status.Code() != fwk.UnschedulableAndUnresolvable || !strings.Contains(status.Message(), "worker") || !strings.Contains(status.Message(), "spec.topologySpreadConstraints") {
		t.Fatalf("group rejection: %v", status)
	}
	_, status = sb.PostFilter(context.Background(), state, leader, nil)
	if status.Code() != fwk.UnschedulableAndUnresolvable {
		t.Fatalf("PostFilter after rejected group: %v", status)
	}
	for _, original := range []*corev1.Pod{leader, worker} {
		actual := &corev1.Pod{}
		if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(original), actual); err != nil {
			t.Fatal(err)
		}
		if len(actual.Labels) != 1 || len(actual.Annotations) != 0 || actual.Spec.NodeName != "" {
			t.Fatalf("rejected group member was modified: %v", actual)
		}
	}
}

func TestNativeSchedulingPolicyRejectsUnsupportedSibling(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "running"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			sb, control, pods, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
			control.job.Pending, control.job.Held = pending, pending
			control.updateJob = func(*slurmjobir.SlurmJobIR) (int32, error) {
				t.Fatal("unsupported gang reached Slurm update")
				return 0, nil
			}
			worker := &corev1.Pod{}
			if err := sb.Get(t.Context(), client.ObjectKeyFromObject(pods[1]), worker); err != nil {
				t.Fatal(err)
			}
			worker.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway}}
			if err := sb.Update(t.Context(), worker); err != nil {
				t.Fatal(err)
			}
			state := nativePodState(framework.NewCycleState())
			_, before := sb.PreFilter(t.Context(), state, pods[0], nil)
			if before.Code() != fwk.UnschedulableAndUnresolvable || !strings.Contains(before.Message(), "spec.topologySpreadConstraints") {
				t.Fatalf("native sibling rejection: %v", before)
			}
			_, after := sb.PostFilter(t.Context(), state, pods[0], nil)
			if after.Code() != before.Code() || after.Message() != before.Message() {
				t.Fatalf("native rejection lost: %v to %v", before, after)
			}
			if control.liveReads.Load() != 0 || control.resourceReads.Load() != 0 || control.releaseCalls != 0 || control.deleteCalls != 0 {
				t.Fatal("unsupported gang reached Slurm placement or release")
			}
		})
	}
}

func TestSchedulingPolicyHookMatrix(t *testing.T) {
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
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unsupported"}, Spec: tc.spec}
			sb := &SlurmBridge{} // Rejection must precede any Kubernetes or Slurm call.
			state := framework.NewCycleState()
			if _, status := sb.PreFilter(context.Background(), state, pod, nil); status.Code() != fwk.UnschedulableAndUnresolvable {
				t.Fatalf("PreFilter: %v", status)
			}
			if _, status := sb.PostFilter(context.Background(), state, pod, nil); status.Code() != fwk.UnschedulableAndUnresolvable {
				t.Fatalf("PostFilter: %v", status)
			}
		})
	}
}
