// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
)

func newJobSet(name string) *jobset.JobSet {
	return &jobset.JobSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
		},
	}
}

func Test_translator_fromJobSet(t *testing.T) {
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
			name: "JobSet does not exist",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(kubescheme.AddToScheme(scheme))
					utilruntime.Must(batchv1.AddToScheme(scheme))
					utilruntime.Must(jobset.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newJobSet("foo"),
						newJob("foo"),
						newJobPod("foo", "bar"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: newJobPod("foo", "bar"),
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Job does not exist",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(kubescheme.AddToScheme(scheme))
					utilruntime.Must(batchv1.AddToScheme(scheme))
					utilruntime.Must(jobset.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newJobSet("foo"),
						newJob("foo"),
						newJobPod("foo", "bar"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: newJobPod("foo", "bar"),
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "foo",
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "JobSet to SlurmJobIR",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(kubescheme.AddToScheme(scheme))
					utilruntime.Must(batchv1.AddToScheme(scheme))
					utilruntime.Must(jobset.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newJobSet("foo"),
						newJob("foo"),
						newJobPod("foo", "foo"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: newJobPod("foo", "foo"),
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "foo",
					},
				},
			},
			want: &SlurmJobIR{
				Components: []SlurmJobComponent{
					{
						JobInfo: SlurmJobIRJobInfo{
							MinNodes:   ptr.To(int32(1)),
							CpuPerTask: ptr.To(int32(22)),
							MemPerNode: ptr.To(int64(1)),
						},
						Pods: corev1.PodList{
							Items: []corev1.Pod{
								*newJobPod("foo", "foo"),
							},
						},
					},
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
			got, err := tr.fromJobSet(tt.args.pod, tt.args.rootPOM)
			if (err != nil) != tt.wantErr {
				t.Errorf("translator.fromJobSet() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("translator.fromJobSet() = %v, want %v", got, tt.want)
			}
		})
	}
}
