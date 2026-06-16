// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	batchv1 "k8s.io/api/batch/v1"
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

func jobWithAnnotations(name, ns string, ann map[string]string) *batchv1.Job {
	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: batchv1.SchemeGroupVersion.String(),
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: ann,
		},
	}
}

func podWithJobOwner(pod *corev1.Pod, jobName string) *corev1.Pod {
	out := pod.DeepCopy()
	out.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: batchv1.SchemeGroupVersion.String(),
		Kind:       "Job",
		Name:       jobName,
		Controller: ptr.To(true),
	}}
	return out
}

func TestTranslateToSlurmJobIR_PodGroupAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1alpha2.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))

	workloadRef := &schedulingv1alpha2.PodGroupTemplateReference{
		Workload: &schedulingv1alpha2.WorkloadPodGroupTemplateReference{
			WorkloadName:         "my-workload",
			PodGroupTemplateName: "workers",
		},
	}

	tests := []struct {
		name          string
		objects       []client.Object
		wantJobName   string
		wantTimeLimit *int32
		wantPartition *string
		wantQOS       *string
		wantAccount   *string
	}{
		{
			name: "workload overrides job and podgroup",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Annotations = map[string]string{wellknown.AnnotationTimeLimit: "10"}
					pg.Spec.PodGroupTemplateRef = workloadRef
					return pg
				}(),
				&schedulingv1alpha2.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-workload", Namespace: "default",
						Annotations: map[string]string{wellknown.AnnotationTimeLimit: "30"},
					},
				},
				jobWithAnnotations("my-job", "default", map[string]string{
					wellknown.AnnotationJobName:   "my-job",
					wellknown.AnnotationTimeLimit: "5",
				}),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "pg1"), "my-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "pg1"), "my-job"),
			},
			wantJobName:   "my-job",
			wantTimeLimit: ptr.To(int32(30)),
		},
		{
			name: "job overrides podgroup without workload ref",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Annotations = map[string]string{wellknown.AnnotationTimeLimit: "10"}
					return pg
				}(),
				jobWithAnnotations("my-job", "default", map[string]string{
					wellknown.AnnotationTimeLimit: "5",
				}),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "pg1"), "my-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "pg1"), "my-job"),
			},
			wantJobName:   "pg1",
			wantTimeLimit: ptr.To(int32(5)),
		},
		{
			name: "podgroup only when job has no annotations",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Annotations = map[string]string{
						wellknown.AnnotationTimeLimit: "10",
						wellknown.AnnotationPartition: "pg-partition",
					}
					return pg
				}(),
				jobWithAnnotations("my-job", "default", nil),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "pg1"), "my-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "pg1"), "my-job"),
			},
			wantJobName:   "pg1",
			wantTimeLimit: ptr.To(int32(10)),
			wantPartition: ptr.To("pg-partition"),
		},
		{
			name: "missing workload falls back to job over podgroup",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Annotations = map[string]string{wellknown.AnnotationTimeLimit: "10"}
					pg.Spec.PodGroupTemplateRef = workloadRef
					return pg
				}(),
				jobWithAnnotations("my-job", "default", map[string]string{
					wellknown.AnnotationTimeLimit: "5",
				}),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "pg1"), "my-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "pg1"), "my-job"),
			},
			wantJobName:   "pg1",
			wantTimeLimit: ptr.To(int32(5)),
		},
		{
			name: "non-conflicting annotations from each layer",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Annotations = map[string]string{wellknown.AnnotationPartition: "pg-partition"}
					pg.Spec.PodGroupTemplateRef = workloadRef
					return pg
				}(),
				&schedulingv1alpha2.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-workload", Namespace: "default",
						Annotations: map[string]string{wellknown.AnnotationAccount: "wl-account"},
					},
				},
				jobWithAnnotations("my-job", "default", map[string]string{
					wellknown.AnnotationQOS: "job-qos",
				}),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "pg1"), "my-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "pg1"), "my-job"),
			},
			wantJobName:   "pg1",
			wantPartition: ptr.To("pg-partition"),
			wantQOS:       ptr.To("job-qos"),
			wantAccount:   ptr.To("wl-account"),
		},
		{
			name: "workload wins on same key as job and podgroup",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("pg1", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Annotations = map[string]string{wellknown.AnnotationPartition: "pg-partition"}
					pg.Spec.PodGroupTemplateRef = workloadRef
					return pg
				}(),
				&schedulingv1alpha2.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-workload", Namespace: "default",
						Annotations: map[string]string{wellknown.AnnotationPartition: "wl-partition"},
					},
				},
				jobWithAnnotations("my-job", "default", map[string]string{
					wellknown.AnnotationPartition: "job-partition",
				}),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "pg1"), "my-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "pg1"), "my-job"),
			},
			wantJobName:   "pg1",
			wantPartition: ptr.To("wl-partition"),
		},
		{
			name: "workload job-name overrides podgroup object name when they differ",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("training-job-workers", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Spec.PodGroupTemplateRef = &schedulingv1alpha2.PodGroupTemplateReference{
						Workload: &schedulingv1alpha2.WorkloadPodGroupTemplateReference{
							WorkloadName:         "training-workload",
							PodGroupTemplateName: "workers",
						},
					}
					return pg
				}(),
				&schedulingv1alpha2.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name: "training-workload", Namespace: "default",
						Annotations: map[string]string{
							wellknown.AnnotationJobName: "training-job",
						},
					},
				},
				jobWithAnnotations("training-job", "default", nil),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "training-job-workers"), "training-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "training-job-workers"), "training-job"),
			},
			wantJobName: "training-job",
		},
		{
			name: "workload job-name overrides podgroup job-name annotation",
			objects: []client.Object{
				func() *schedulingv1alpha2.PodGroup {
					pg := newPodGroup("training-job-workers", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
					})
					pg.Annotations = map[string]string{
						wellknown.AnnotationJobName: "pg-slurm-name",
					}
					pg.Spec.PodGroupTemplateRef = &schedulingv1alpha2.PodGroupTemplateReference{
						Workload: &schedulingv1alpha2.WorkloadPodGroupTemplateReference{
							WorkloadName:         "training-workload",
							PodGroupTemplateName: "workers",
						},
					}
					return pg
				}(),
				&schedulingv1alpha2.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name: "training-workload", Namespace: "default",
						Annotations: map[string]string{
							wellknown.AnnotationJobName: "training-job",
						},
					},
				},
				jobWithAnnotations("training-job", "default", nil),
				podWithJobOwner(podWithSchedulingGroup("default", "p1", "training-job-workers"), "training-job"),
				podWithJobOwner(podWithSchedulingGroup("default", "p2", "training-job-workers"), "training-job"),
			},
			wantJobName: "training-job",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, len(tt.objects))
			for i, obj := range tt.objects {
				objs[i] = obj.DeepCopyObject().(client.Object)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

			pod := tt.objects[len(tt.objects)-2].(*corev1.Pod).DeepCopy()
			got, err := TranslateToSlurmJobIR(cl, context.TODO(), pod)
			if err != nil {
				t.Fatalf("TranslateToSlurmJobIR() error = %v", err)
			}
			if tt.wantJobName != "" {
				if got.JobInfo.JobName == nil || *got.JobInfo.JobName != tt.wantJobName {
					t.Errorf("JobName = %q, want %q", ptr.Deref(got.JobInfo.JobName, ""), tt.wantJobName)
				}
			}
			if tt.wantTimeLimit != nil {
				if got.JobInfo.TimeLimit == nil || *got.JobInfo.TimeLimit != *tt.wantTimeLimit {
					t.Errorf("TimeLimit = %v, want %v", got.JobInfo.TimeLimit, *tt.wantTimeLimit)
				}
			}
			if tt.wantPartition != nil {
				if got.JobInfo.Partition == nil || *got.JobInfo.Partition != *tt.wantPartition {
					t.Errorf("Partition = %v, want %v", got.JobInfo.Partition, *tt.wantPartition)
				}
			}
			if tt.wantQOS != nil {
				if got.JobInfo.QOS == nil || *got.JobInfo.QOS != *tt.wantQOS {
					t.Errorf("QOS = %v, want %v", got.JobInfo.QOS, *tt.wantQOS)
				}
			}
			if tt.wantAccount != nil {
				if got.JobInfo.Account == nil || *got.JobInfo.Account != *tt.wantAccount {
					t.Errorf("Account = %v, want %v", got.JobInfo.Account, *tt.wantAccount)
				}
			}
		})
	}
}
