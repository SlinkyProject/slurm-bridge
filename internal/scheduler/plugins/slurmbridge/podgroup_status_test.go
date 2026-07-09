// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1alpha2.AddToScheme(scheme))

	const (
		namespace = "slurm-bridge"
		pgName    = "podgroup"
	)
	pg := &schedulingv1alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName},
		Status: schedulingv1alpha2.PodGroupStatus{
			Conditions: []metav1.Condition{{
				Type:   schedulingv1alpha2.PodGroupScheduled,
				Status: metav1.ConditionTrue,
			}},
		},
	}

	podGets := 0
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pg).
		WithStatusSubresource(&schedulingv1alpha2.PodGroup{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Pod); ok {
					podGets++
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	sb := &SlurmBridge{Client: kubeClient}
	ir := &slurmjobir.SlurmJobIR{
		RootPOM: metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha2", Kind: "PodGroup"},
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName},
		},
		Pods: corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "pod-a"},
		}}},
	}

	sb.markPodGroupScheduled(ctx, ir, "5")
	if podGets != 0 {
		t.Fatalf("pod GETs = %d, want 0 for an already scheduled PodGroup", podGets)
	}
}
