// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"errors"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

func newPodGroup(name string, minMembers int32, status sched.PodGroupStatus) *sched.PodGroup {
	return &sched.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       metav1.NamespaceDefault,
			Name:            name,
			ResourceVersion: "999",
		},
		Spec: sched.PodGroupSpec{
			MinResources: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewQuantity(22, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(1024*1024, resource.DecimalSI),
			},
			MinMember: minMembers,
		},
		Status: status,
	}
}

func newPodGroupPod(name, podGroupName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
			Labels: map[string]string{
				sched.PodGroupLabel: podGroupName,
			},
			ResourceVersion: "999",
		},
	}
}

func Test_translator_GetPodGroup(t *testing.T) {
	type fields struct {
		Reader client.Reader
		ctx    context.Context
	}
	type args struct {
		pod *corev1.Pod
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   string
		want1  *sched.PodGroup
	}{
		{
			name: "No PodGroup",
			fields: fields{
				Reader: nil,
				ctx:    context.Background(),
			},
			args: args{
				pod: &corev1.Pod{},
			},
			want:  "",
			want1: nil,
		},
		{
			name: "PodGroup foo",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("foo", int32(1), sched.PodGroupStatus{}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: newPodGroupPod("foo", "foo"),
			},
			want:  "default/foo",
			want1: newPodGroup("foo", int32(1), sched.PodGroupStatus{}),
		},
		{
			name: "PodGroup foo does not exist",
			fields: fields{
				Reader: fake.NewFakeClient(),
				ctx:    context.Background(),
			},
			args: args{
				pod: newPodGroupPod("foo", "foo"),
			},
			want:  "default/foo",
			want1: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &translator{
				Reader: tt.fields.Reader,
				ctx:    tt.fields.ctx,
			}
			got, got1 := tr.GetPodGroup(tt.args.pod)
			if got != tt.want {
				t.Errorf("translator.GetPodGroup() got = %v, want %v", got, tt.want)
			}
			if !apiequality.Semantic.DeepEqual(got1, tt.want1) {
				t.Errorf("translator.GetPodGroup() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func Test_translator_fromPodGroup(t *testing.T) {
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
			name: "PodGroup does not exist",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					utilruntime.Must(corev1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroupPod("foo", "foo"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: newPodGroupPod("foo", "foo"),
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
			name: "Fails to list pods",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					utilruntime.Must(corev1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("foo", int32(1), sched.PodGroupStatus{}),
						newPodGroupPod("foo", "foo"),
					).WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							return errors.New("Failed to List")
						},
					}).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: newPodGroupPod("foo", "foo"),
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
			name: "Test podgroup",
			fields: fields{
				Reader: func() client.Reader {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					utilruntime.Must(corev1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("foo", int32(1), sched.PodGroupStatus{}),
						newPodGroupPod("foo", "foo"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: newPodGroupPod("foo", "foo"),
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want: &SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					CpuPerTask:   ptr.To((int32(22))),
					MemPerNode:   ptr.To((int64(1))),
					MinNodes:     ptr.To((int32(1))),
					MaxNodes:     ptr.To((int32(1))),
					TasksPerNode: ptr.To((int32(1))),
				},
				Pods: corev1.PodList{
					Items: []corev1.Pod{
						*newPodGroupPod("foo", "foo"),
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
			got, err := tr.fromPodGroup(tt.args.pod, tt.args.rootPOM)
			if (err != nil) != tt.wantErr {
				t.Errorf("translator.fromPodGroup() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("translator.fromPodGroup() = %v, want %v", got, &tt.want)
			}
		})
	}
}

func Test_translator_PreFilterPodGroup(t *testing.T) {
	type fields struct {
		Reader client.Reader
		ctx    context.Context
	}
	type args struct {
		slurmJobIR *SlurmJobIR
		pod        *corev1.Pod
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *framework.Status
	}{
		{
			name: "Could not get PodGroup",
			fields: fields{
				Reader: fake.NewFakeClient(),
				ctx:    context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: "default",
						},
					},
				},
			},
			want: framework.NewStatus(framework.Error, ErrorPodGroupCouldNotGet.Error()),
		},
		{
			name: "PodGroup phase is Failed",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("pg", 3, sched.PodGroupStatus{Phase: sched.PodGroupFailed}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: metav1.NamespaceDefault,
						},
					},
				},
			},
			want: framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupFailed.Error()),
		},
		{
			name: "PodGroup phase is Finished",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("pg", 3, sched.PodGroupStatus{Phase: sched.PodGroupFinished}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: metav1.NamespaceDefault,
						},
					},
				},
			},
			want: framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupFinished.Error()),
		},
		{
			name: "PodGroup phase is Running",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("pg", 3, sched.PodGroupStatus{Phase: sched.PodGroupRunning}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: metav1.NamespaceDefault,
						},
					},
				},
			},
			want: framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupRunning.Error()),
		},
		{
			name: "PodGroup phase is Unknown",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("pg", 3, sched.PodGroupStatus{Phase: sched.PodGroupUnknown}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: metav1.NamespaceDefault,
						},
					},
				},
			},
			want: framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorPodGroupUnknown.Error()),
		},
		{
			name: "Not enough pods for minmembers",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("pg", 3, sched.PodGroupStatus{}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: "default",
						},
					},
				},
				pod: &corev1.Pod{},
			},
			want: framework.NewStatus(framework.Error, ErrorInsuffientPods.Error()),
		},
		{
			name: "Not enough pods for minmembers and invalid pod",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("pg", 3, sched.PodGroupStatus{}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: metav1.NamespaceDefault,
						},
					},
				},
				pod: func() *corev1.Pod {
					p := newPodGroupPod("foo", "foo")
					p.Labels[wellknown.LabelPlaceholderJobId] = "123"
					return p
				}(),
			},
			want: framework.NewStatus(framework.Error, ErrorPlaceholderJobInvalid.Error()),
		},
		{
			name: "Enough pods for minmembers",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(sched.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newPodGroup("pg", 1, sched.PodGroupStatus{}),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "pg",
							Namespace: "default",
						},
					},
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							*newPodGroupPod("pod", "pg"),
						},
					},
				},
				pod: &corev1.Pod{},
			},
			want: framework.NewStatus(framework.Success),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &translator{
				Reader: tt.fields.Reader,
				ctx:    tt.fields.ctx,
			}
			if got := tr.PreFilterPodGroup(tt.args.pod, tt.args.slurmJobIR); !got.Equal(tt.want) {
				t.Errorf("translator.PreFilterPodGroup() = %v, want %v", got, tt.want)
			}
		})
	}
}
