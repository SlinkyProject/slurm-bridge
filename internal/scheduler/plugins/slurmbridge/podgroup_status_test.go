// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func Test_podsHaveSlurmNodeAssignments(t *testing.T) {
	t.Parallel()
	pods := &corev1.PodList{
		Items: []corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{wellknown.LabelExternalJobId: "1"},
					Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node-a"},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
				},
			},
		},
	}
	if podsHaveSlurmNodeAssignments(pods, "1") {
		t.Fatal("expected false when a pod lacks node annotation")
	}
	pods.Items[1].Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node-b"}
	if !podsHaveSlurmNodeAssignments(pods, "1") {
		t.Fatal("expected true when all pods have job id and node")
	}
	if podsHaveSlurmNodeAssignments(pods, "2") {
		t.Fatal("expected false when job id does not match")
	}
}

func TestMarkPodGroupScheduledSkipsPodRefreshWhenAlreadyScheduled(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Alpha2)

	const (
		namespace = "slurm-bridge"
		pgName    = "podgroup"
	)
	pg := &slurmjobir.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName},
		Status: slurmjobir.PodGroupStatus{
			Conditions: []metav1.Condition{{
				Type:   workloadAPI.ScheduledCondition,
				Status: metav1.ConditionTrue,
			}},
		},
	}

	podGets := 0
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pg).
		WithStatusSubresource(&slurmjobir.PodGroup{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Pod); ok {
					podGets++
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	sb := &SlurmBridge{Client: kubeClient, workloadAPI: workloadAPI}
	component := slurmjobir.SlurmJobComponent{
		Pods: corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "pod-a"},
		}}},
	}
	ir := &slurmjobir.SlurmJobIR{
		RootPOM: metav1.PartialObjectMetadata{
			TypeMeta:   workloadAPI.PodGroupTypeMeta,
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName},
		},
		Components: []slurmjobir.SlurmJobComponent{component},
	}

	sb.markPodGroupScheduled(ctx, ir, &ir.Components[0], "5")
	if podGets != 0 {
		t.Fatalf("pod GETs = %d, want 0 for an already scheduled PodGroup", podGets)
	}
}

func TestMarkPodGroupScheduledBeta(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Beta1)

	const (
		namespace = "slurm-bridge"
		pgName    = "podgroup"
		jobID     = "5"
	)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "pod-a",
			Labels:    map[string]string{wellknown.LabelExternalJobId: jobID},
			Annotations: map[string]string{
				wellknown.AnnotationExternalJobNode: "node-a",
			},
		},
	}
	pg := &slurmjobir.PodGroup{
		TypeMeta: workloadAPI.PodGroupTypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      pgName,
		},
	}
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pg, pod).
		WithStatusSubresource(&slurmjobir.PodGroup{}).
		Build()
	sb := &SlurmBridge{
		Client:        kubeClient,
		schedulerName: "slurm-bridge-scheduler",
		workloadAPI:   workloadAPI,
	}
	ir := &slurmjobir.SlurmJobIR{
		RootPOM: metav1.PartialObjectMetadata{
			TypeMeta:   pg.TypeMeta,
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName},
		},
		Components: []slurmjobir.SlurmJobComponent{{
			Pods: corev1.PodList{Items: []corev1.Pod{*pod.DeepCopy()}},
		}},
	}

	sb.markPodGroupScheduled(ctx, ir, &ir.Components[0], jobID)

	updated := &slurmjobir.PodGroup{TypeMeta: pg.TypeMeta}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(pg), updated); err != nil {
		t.Fatalf("Get PodGroup: %v", err)
	}
	condition := updated.Status.Conditions
	if len(condition) != 1 || condition[0].Type != "PodGroupInitiallyScheduled" || condition[0].Status != metav1.ConditionTrue {
		t.Fatalf("PodGroup conditions = %#v, want PodGroupInitiallyScheduled true", condition)
	}
}

func TestMarkPodGroupScheduledPreservesConcurrentConditions(t *testing.T) {
	for _, version := range []string{slurmjobir.WorkloadAPIVersionV1Alpha2, slurmjobir.WorkloadAPIVersionV1Beta1} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			utilruntime.Must(corev1.AddToScheme(scheme))
			workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, version)
			pg := &slurmjobir.PodGroup{
				TypeMeta:   workloadAPI.PodGroupTypeMeta,
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "podgroup"},
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   pg.Namespace,
					Name:        "pod-a",
					Labels:      map[string]string{wellknown.LabelExternalJobId: "5"},
					Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node-a"},
				},
			}
			concurrentCondition := metav1.Condition{
				Type:               "DisruptionTarget",
				Status:             metav1.ConditionTrue,
				Reason:             "PreemptionByScheduler",
				Message:            "PodGroup was preempted",
				LastTransitionTime: metav1.Now(),
			}
			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(pg, pod).
				WithStatusSubresource(pg).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.Pod); ok {
							// Another writer updates the PodGroup after the scheduler's
							// initial read, while it refreshes the assigned pods.
							current := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta}
							if err := c.Get(ctx, client.ObjectKeyFromObject(pg), current); err != nil {
								return err
							}
							apimeta.SetStatusCondition(&current.Status.Conditions, concurrentCondition)
							if err := c.Status().Update(ctx, current); err != nil {
								return err
							}
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).
				Build()
			sb := &SlurmBridge{
				Client:        kubeClient,
				schedulerName: "slurm-bridge-scheduler",
				workloadAPI:   workloadAPI,
			}
			ir := &slurmjobir.SlurmJobIR{
				RootPOM: metav1.PartialObjectMetadata{TypeMeta: pg.TypeMeta, ObjectMeta: pg.ObjectMeta},
				Components: []slurmjobir.SlurmJobComponent{{
					Pods: corev1.PodList{Items: []corev1.Pod{*pod.DeepCopy()}},
				}},
			}

			sb.markPodGroupScheduled(ctx, ir, &ir.Components[0], "5")

			updated := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta}
			if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(pg), updated); err != nil {
				t.Fatalf("Get PodGroup: %v", err)
			}
			if condition := apimeta.FindStatusCondition(updated.Status.Conditions, workloadAPI.ScheduledCondition); condition == nil || condition.Status != metav1.ConditionTrue {
				t.Fatalf("scheduled condition = %#v, want true", condition)
			}
			if condition := apimeta.FindStatusCondition(updated.Status.Conditions, concurrentCondition.Type); condition == nil || condition.Status != concurrentCondition.Status || condition.Reason != concurrentCondition.Reason || condition.Message != concurrentCondition.Message {
				t.Fatalf("concurrent condition = %#v, want %#v", condition, concurrentCondition)
			}
		})
	}
}

func TestMarkPodGroupScheduledRefreshesComponentInPlace(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Alpha2)

	const (
		namespace = "slurm-bridge"
		pgName    = "podgroup"
		podName   = "pod-a"
		jobID     = "5"
	)
	pg := &slurmjobir.PodGroup{
		TypeMeta: workloadAPI.PodGroupTypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  namespace,
			Name:       pgName,
			Generation: 3,
		},
	}
	refreshedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      podName,
			Labels: map[string]string{
				wellknown.LabelExternalJobId: jobID,
			},
			Annotations: map[string]string{
				wellknown.AnnotationExternalJobNode: "node-a",
			},
		},
	}
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pg, refreshedPod).
		WithStatusSubresource(&slurmjobir.PodGroup{}).
		Build()
	sb := &SlurmBridge{
		Client:        kubeClient,
		schedulerName: "slurm-bridge-scheduler",
		workloadAPI:   workloadAPI,
	}
	ir := &slurmjobir.SlurmJobIR{
		RootPOM: metav1.PartialObjectMetadata{
			TypeMeta: workloadAPI.PodGroupTypeMeta,
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      pgName,
			},
		},
		Components: []slurmjobir.SlurmJobComponent{{
			Pods: corev1.PodList{Items: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      podName,
				},
			}}},
		}},
	}

	sb.markPodGroupScheduled(ctx, ir, &ir.Components[0], jobID)

	gotPod := ir.Components[0].Pods.Items[0]
	if got := gotPod.Annotations[wellknown.AnnotationExternalJobNode]; got != "node-a" {
		t.Errorf("refreshed IR pod node annotation = %q, want node-a", got)
	}
	updated := &slurmjobir.PodGroup{TypeMeta: workloadAPI.PodGroupTypeMeta}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(pg), updated); err != nil {
		t.Fatalf("Get(PodGroup) error = %v", err)
	}
	condition := apimeta.FindStatusCondition(updated.Status.Conditions, workloadAPI.ScheduledCondition)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		t.Errorf("scheduled condition = %#v, want true after in-place pod refresh", condition)
	}
}
