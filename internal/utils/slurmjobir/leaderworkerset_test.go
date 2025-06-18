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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func newLWS(name string, size int32) *lwsv1.LeaderWorkerSet {
	return &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       metav1.NamespaceDefault,
			Name:            name,
			ResourceVersion: "999",
		},
		Spec: lwsv1.LeaderWorkerSetSpec{
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size: ptr.To(size),
			},
		},
	}
}

func newLWSPod(name, groupHash string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
			Labels: map[string]string{
				lwsv1.GroupUniqueHashLabelKey: groupHash,
			},
			ResourceVersion: "999",
		},
	}
}

func Test_translator_PreFilterLWS(t *testing.T) {
	type fields struct {
		Reader client.Reader
		ctx    context.Context
	}
	type args struct {
		pod        *corev1.Pod
		slurmJobIR *SlurmJobIR
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *framework.Status
	}{
		{
			name: "Fail to get LWS",
			fields: fields{
				Reader: fake.NewFakeClient(),
				ctx:    context.Background(),
			},
			args: args{
				pod:        &corev1.Pod{},
				slurmJobIR: &SlurmJobIR{},
			},
			want: framework.NewStatus(framework.Error, ErrorLWSCouldNotGet.Error()),
		},
		{
			name: "Not enough pods for group",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "lws",
							Namespace: corev1.NamespaceDefault,
						},
					},
					Pods: corev1.PodList{},
				},
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
			},
			want: framework.NewStatus(framework.Error, ErrorInsuffientPods.Error()),
		},
		{
			name: "Invalid state with placeholder and insufficient pods",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "lws",
							Namespace: corev1.NamespaceDefault,
						},
					},
					Pods: corev1.PodList{},
				},
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey:   "lws",
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
			},
			want: framework.NewStatus(framework.Error, ErrorPlaceholderJobInvalid.Error()),
		},
		{
			name: "group has enough pods",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "lws",
							Namespace: corev1.NamespaceDefault,
						},
					},
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							*newLWSPod("pod1", "lws"),
							*newLWSPod("pod2", "lws"),
						},
					},
				},
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
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
			got := tr.PreFilterLWS(tt.args.pod, tt.args.slurmJobIR)
			if !got.Equal(tt.want) {
				t.Errorf("translator.PreFilterLWS() = %v, want %v", got.AsError(), tt.want.AsError())
			}
		})
	}
}

func Test_translator_fromLws(t *testing.T) {
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
			name: "Fail to get LWS",
			fields: fields{
				Reader: fake.NewFakeClient(),
				ctx:    context.Background(),
			},
			args: args{
				pod: &corev1.Pod{},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Could not list pods",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							return errors.New("Failed to List")
						},
					}).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "No LWS pods found",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(corev1.AddToScheme(scheme))
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "LWS to SlurmJobIR",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(corev1.AddToScheme(scheme))
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
						newLWSPod("pod1", "lws"),
						newLWSPod("pod2", "lws"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
							lwsv1.SetNameLabelKey:         "foo",
							lwsv1.GroupIndexLabelKey:      "1",
						},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want: &SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					JobName:      ptr.To("foo-1"),
					MaxNodes:     ptr.To(int32(2)),
					MinNodes:     ptr.To(int32(2)),
					TasksPerNode: ptr.To(int32(1)),
				},
				Pods: corev1.PodList{
					Items: []corev1.Pod{
						*newLWSPod("pod1", "lws"),
						*newLWSPod("pod2", "lws"),
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
			got, err := tr.fromLws(tt.args.pod, tt.args.rootPOM)
			if (err != nil) != tt.wantErr {
				t.Errorf("translator.fromLws() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("translator.fromLws() = %v, want %v", got, tt.want)
			}
		})
	}
}
