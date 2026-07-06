// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newJob(name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Resources: &corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewQuantity(22, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(1024*1024, resource.DecimalSI),
						},
					},
				},
			},
		},
	}
}

func newJobPod(name, jobName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
			Labels: map[string]string{
				"job-name": jobName,
			},
		},
	}
}

func Test_translator_fromJob(t *testing.T) {
	type fields struct {
		Reader client.Reader
		ctx    context.Context
	}
	type args struct {
		pod     *corev1.Pod
		rootPOM *metav1.PartialObjectMetadata
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *SlurmJobIR
		wantErr bool
	}{
		{
			name: "Job does not exist",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(kubescheme.AddToScheme(scheme))
					utilruntime.Must(batchv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newJob("wrongJob"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Job to SlurmJobIR",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(kubescheme.AddToScheme(scheme))
					utilruntime.Must(batchv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newJob("foo"),
						newJobPod("foo", "foo"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
						Labels:    map[string]string{batchv1.JobNameLabel: "foo"},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want: &SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					MinNodes:   ptr.To(int32(1)),
					CpuPerTask: ptr.To(int32(22)),
					MemPerNode: ptr.To(int64(1)),
				},
				Pods: corev1.PodList{
					Items: []corev1.Pod{{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: metav1.NamespaceDefault,
							Labels:    map[string]string{batchv1.JobNameLabel: "foo"},
						},
					}},
				},
			},
			wantErr: false,
		},
		{
			name: "Job with activeDeadlineSeconds",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(kubescheme.AddToScheme(scheme))
					utilruntime.Must(batchv1.AddToScheme(scheme))
					job := newJob("foo")
					job.Spec.ActiveDeadlineSeconds = ptr.To(int64(90))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						job,
						newJobPod("foo", "foo"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
						Labels:    map[string]string{batchv1.JobNameLabel: "foo"},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want: &SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					MinNodes:   ptr.To(int32(1)),
					CpuPerTask: ptr.To(int32(22)),
					MemPerNode: ptr.To(int64(1)),
					TimeLimit:  ptr.To(int32(2)), // 90s rounds up to 2 min
				},
				Pods: corev1.PodList{
					Items: []corev1.Pod{{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: metav1.NamespaceDefault,
							Labels:    map[string]string{batchv1.JobNameLabel: "foo"},
						},
					}},
				},
			},
			wantErr: false,
		},
		{
			name: "Job with zero activeDeadlineSeconds leaves TimeLimit unset",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(kubescheme.AddToScheme(scheme))
					utilruntime.Must(batchv1.AddToScheme(scheme))
					job := newJob("foo")
					job.Spec.ActiveDeadlineSeconds = ptr.To(int64(0))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						job,
						newJobPod("foo", "foo"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
						Labels:    map[string]string{batchv1.JobNameLabel: "foo"},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want: &SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					MinNodes:   ptr.To(int32(1)),
					CpuPerTask: ptr.To(int32(22)),
					MemPerNode: ptr.To(int64(1)),
					// TimeLimit intentionally unset: 0 in Slurm means unlimited.
				},
				Pods: corev1.PodList{
					Items: []corev1.Pod{{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: metav1.NamespaceDefault,
							Labels:    map[string]string{batchv1.JobNameLabel: "foo"},
						},
					}},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &translator{
				Reader: tt.fields.Reader,
				ctx:    tt.fields.ctx,
			}
			got, err := tr.fromJob(tt.args.pod, tt.args.rootPOM)
			if (err != nil) != tt.wantErr {
				t.Errorf("translator.fromJob() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("translator.fromJob() = %v, want %v", got, &tt.want)
			}
		})
	}
}
