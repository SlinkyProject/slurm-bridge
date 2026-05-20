// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func podWithSchedulingGroup(ns, name, pgName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{
			SchedulingGroup: &corev1.PodSchedulingGroup{
				PodGroupName: ptr.To(pgName),
			},
		},
	}
}

func newPodGroup(name, ns string, policy schedulingv1alpha2.PodGroupSchedulingPolicy) *schedulingv1alpha2.PodGroup {
	return &schedulingv1alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: schedulingv1alpha2.PodGroupSpec{
			SchedulingPolicy: policy,
		},
	}
}

func Test_translator_fromPodGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1alpha2.AddToScheme(scheme))

	type args struct {
		pod     *corev1.Pod
		rootPOM *metav1.PartialObjectMetadata
	}
	tests := []struct {
		name    string
		client  client.Client
		args    args
		wantErr bool
	}{
		{
			name: "two pods same scheduling group",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
					Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
				}),
				podWithSchedulingGroup("default", "p1", "pg1"),
				podWithSchedulingGroup("default", "p2", "pg1"),
			).Build(),
			args: args{
				pod: podWithSchedulingGroup("default", "p1", "pg1"),
				rootPOM: &metav1.PartialObjectMetadata{
					TypeMeta:   podgroup_v1alpha2,
					ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pg1"},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := translator{Reader: tt.client, ctx: context.TODO()}
			got, err := tr.fromPodGroup(tt.args.pod, tt.args.rootPOM)
			if (err != nil) != tt.wantErr {
				t.Fatalf("fromPodGroup() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got.Pods.Items) != 2 {
				t.Errorf("fromPodGroup() len(pods) = %d, want 2", len(got.Pods.Items))
			}
			if got.JobInfo.JobName == nil || *got.JobInfo.JobName != "pg1" {
				t.Errorf("fromPodGroup() JobName = %v, want pg1", got.JobInfo.JobName)
			}
		})
	}
}

func Test_translator_PreFilterPodGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1alpha2.AddToScheme(scheme))

	pg := newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
		Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
	})
	p1 := podWithSchedulingGroup("default", "p1", "pg1")
	p2 := podWithSchedulingGroup("default", "p2", "pg1")

	type args struct {
		pod        *corev1.Pod
		slurmJobIR *SlurmJobIR
	}
	tests := []struct {
		name   string
		client client.Client
		args   args
		want   *fwk.Status
	}{
		{
			name:   "gang satisfied",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pg.DeepCopy()).Build(),
			args: args{
				pod: p1.DeepCopy(),
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						TypeMeta: podgroup_v1alpha2,
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pg1",
						},
					},
					Pods: corev1.PodList{Items: []corev1.Pod{*p1, *p2}},
				},
			},
			want: fwk.NewStatus(fwk.Success),
		},
		{
			name:   "gang not enough pods",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pg.DeepCopy()).Build(),
			args: args{
				pod: p1.DeepCopy(),
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						TypeMeta: podgroup_v1alpha2,
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pg1",
						},
					},
					Pods: corev1.PodList{Items: []corev1.Pod{*p1}},
				},
			},
			want: fwk.NewStatus(fwk.Error, ErrorInsuffientPods.Error()),
		},
		{
			name: "basic policy skips gang count",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				newPodGroup("pg2", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
					Basic: &schedulingv1alpha2.BasicSchedulingPolicy{},
				}),
			).Build(),
			args: args{
				pod: podWithSchedulingGroup("default", "p1", "pg2"),
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						TypeMeta: podgroup_v1alpha2,
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pg2",
						},
					},
					Pods: corev1.PodList{Items: []corev1.Pod{*podWithSchedulingGroup("default", "p1", "pg2")}},
				},
			},
			want: fwk.NewStatus(fwk.Success),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := translator{Reader: tt.client, ctx: context.TODO()}
			got := tr.PreFilterPodGroup(tt.args.pod, tt.args.slurmJobIR)
			if !got.Equal(tt.want) {
				t.Errorf("PreFilterPodGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_schedulingGroupsMatch(t *testing.T) {
	a := &corev1.PodSchedulingGroup{PodGroupName: ptr.To("pg1")}
	b := &corev1.PodSchedulingGroup{PodGroupName: ptr.To("pg1")}
	c := &corev1.PodSchedulingGroup{PodGroupName: ptr.To("pg2")}
	if !schedulingGroupsMatch(a, b) {
		t.Error("expected same group")
	}
	if schedulingGroupsMatch(a, c) {
		t.Error("expected different groups not to match")
	}
}
