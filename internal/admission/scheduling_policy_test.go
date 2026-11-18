// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSchedulingPolicyAdmission(t *testing.T) {
	r := &PodAdmission{SchedulerName: SchedulerName, ManagedNamespaces: []string{namespace}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "preferred", Namespace: namespace}, Spec: corev1.PodSpec{SchedulerName: SchedulerName, Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1}}}}}}
	if _, err := r.ValidateCreate(context.Background(), pod); !apierrors.IsInvalid(err) {
		t.Fatalf("create: %v", err)
	}
	if _, err := r.ValidateUpdate(contextWithAdmissionOperation("UPDATE"), &corev1.Pod{}, pod); !apierrors.IsInvalid(err) {
		t.Fatalf("update: %v", err)
	}
	pod.Spec.NodeName = "node1"
	if _, err := r.ValidateUpdate(contextWithAdmissionOperation("UPDATE"), pod.DeepCopy(), pod); err != nil {
		t.Fatalf("bound metadata update: %v", err)
	}
	pod.Spec.SchedulerName = "custom-scheduler"
	if _, err := r.ValidateCreate(context.Background(), pod); err != nil {
		t.Fatalf("custom scheduler: %v", err)
	}
	pod.Namespace = "unmanaged"
	pod.Spec.SchedulerName = corev1.DefaultSchedulerName
	if _, err := r.ValidateCreate(context.Background(), pod); err != nil {
		t.Fatalf("unmanaged: %v", err)
	}
	pod.Spec.SchedulerName = SchedulerName
	if _, err := r.ValidateCreate(context.Background(), pod); !apierrors.IsInvalid(err) {
		t.Fatalf("explicit bridge: %v", err)
	}
}

func TestNodeNameAdmission(t *testing.T) {
	r := &PodAdmission{SchedulerName: SchedulerName, ManagedNamespaces: []string{namespace}}
	for _, scheduler := range []string{SchedulerName, corev1.DefaultSchedulerName, "custom-scheduler"} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace}, Spec: corev1.PodSpec{SchedulerName: scheduler, NodeName: "target"}}
		wantRejection := scheduler != "custom-scheduler"
		err := r.Default(contextWithAdmissionOperation("CREATE"), pod)
		if apierrors.IsInvalid(err) != wantRejection {
			t.Fatalf("scheduler %s: %v", scheduler, err)
		}
		if pod.Spec.NodeName != "target" {
			t.Fatal("user node intent was erased")
		}
		_, err = r.ValidateCreate(context.Background(), pod)
		if apierrors.IsInvalid(err) != wantRejection {
			t.Fatalf("create for %s: %v", scheduler, err)
		}
		if err := r.Default(contextWithAdmissionOperation("UPDATE"), pod); err != nil {
			t.Fatalf("bound defaulting: %v", err)
		}
	}
}

func TestSchedulingPolicyAdmissionMatrix(t *testing.T) {
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
	r := &PodAdmission{SchedulerName: SchedulerName, ManagedNamespaces: []string{namespace}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unsupported", Namespace: namespace}, Spec: tc.spec}
			pod.Spec.SchedulerName = corev1.DefaultSchedulerName
			if _, err := r.ValidateCreate(context.Background(), pod); !apierrors.IsInvalid(err) {
				t.Fatalf("create: %v", err)
			}
			if _, err := r.ValidateUpdate(contextWithAdmissionOperation("UPDATE"), pod.DeepCopy(), pod); !apierrors.IsInvalid(err) {
				t.Fatalf("unbound retry: %v", err)
			}
			pod.Spec.NodeName = "bound"
			old := pod.DeepCopy()
			pod.Labels = map[string]string{"metadata": "updated"}
			if _, err := r.ValidateUpdate(contextWithAdmissionOperation("UPDATE"), old, pod); err != nil {
				t.Fatalf("bound metadata: %v", err)
			}
			pod.Spec.SchedulerName = "other"
			if _, err := r.ValidateCreate(context.Background(), pod); err != nil {
				t.Fatalf("other scheduler: %v", err)
			}
		})
	}
}

func TestSchedulingPolicyAdmissionAllowsSupportedRequests(t *testing.T) {
	cases := []struct {
		name string
		spec corev1.PodSpec
	}{
		{"host port", corev1.PodSpec{Containers: []corev1.Container{{Ports: []corev1.ContainerPort{{HostPort: 8080}}}}}},
		{"init host port", corev1.PodSpec{InitContainers: []corev1.Container{{Ports: []corev1.ContainerPort{{HostPort: 8080}}}}}},
		{"host networking", corev1.PodSpec{HostNetwork: true}},
		{"PVC", corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}}},
		{"read-only PVC", corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data", ReadOnly: true}}}}}},
	}
	r := &PodAdmission{SchedulerName: SchedulerName, ManagedNamespaces: []string{namespace}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "supported", Namespace: namespace}, Spec: tc.spec}
			pod.Spec.SchedulerName = SchedulerName
			if _, err := r.ValidateCreate(context.Background(), pod); err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := r.ValidateUpdate(contextWithAdmissionOperation("UPDATE"), pod.DeepCopy(), pod); err != nil {
				t.Fatalf("unbound update: %v", err)
			}
		})
	}
}
