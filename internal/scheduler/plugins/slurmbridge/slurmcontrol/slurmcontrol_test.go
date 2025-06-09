// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	v0043 "github.com/SlinkyProject/slurm-client/api/v0043"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	"k8s.io/utils/ptr"
)

func Test_realSlurmControl_DeleteJob(t *testing.T) {
	type fields struct {
		Client    client.Client
		mcsLabel  string
		partition string
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "No jobs to delete",
			fields: fields{
				Client: func() client.Client {
					return fake.NewClientBuilder().
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{},
			},
			wantErr: false,
		},
		{
			name: "Delete job that does not exist",
			fields: fields{
				Client: func() client.Client {
					list := &slurmtypes.V0043JobInfoList{
						Items: []slurmtypes.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								JobId: ptr.To[int32](2),
							}},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelPlaceholderJobId: "1"},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "Delete job",
			fields: fields{
				Client: func() client.Client {
					list := &slurmtypes.V0043JobInfoList{
						Items: []slurmtypes.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								JobId: ptr.To[int32](1),
							}},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelPlaceholderJobId: "1"},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client:    tt.fields.Client,
				mcsLabel:  tt.fields.mcsLabel,
				partition: tt.fields.partition,
			}
			if err := r.DeleteJob(tt.args.ctx, tt.args.pod); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.DeleteJob() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_GetJobsForPods(t *testing.T) {
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *map[string]PlaceholderJob
		wantErr bool
	}{
		{
			name: "No jobs in slurm",
			fields: fields{
				Client: func() client.Client {
					return fake.NewClientBuilder().
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want:    &map[string]PlaceholderJob{},
			wantErr: false,
		},
		{
			name: "List jobs fails",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						List: func(ctx context.Context, list object.ObjectList, opts ...client.ListOption) error {
							return fmt.Errorf("failed to list resources")
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "List jobs",
			fields: fields{
				Client: func() client.Client {
					list := &slurmtypes.V0043JobInfoList{
						Items: []slurmtypes.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]v0043.V0043JobInfoJobState{v0043.V0043JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1, node2"),
							}},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want: &map[string]PlaceholderJob{
				"slurm/pod1": {JobId: 1, Nodes: "node1, node2"},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			got, err := r.GetJobsForPods(tt.args.ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.GetJobsForPods() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetJobsForPods() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_GetJob(t *testing.T) {
	type fields struct {
		Client    client.Client
		partition string
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *PlaceholderJob
		wantErr bool
	}{
		{
			name: "Failed to get job",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
							return fmt.Errorf("failed to get resource")
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelPlaceholderJobId: "1"}).Obj(),
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Job not found",
			fields: fields{
				Client: func() client.Client {
					list := &slurmtypes.V0043JobInfoList{
						Items: []slurmtypes.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]v0043.V0043JobInfoJobState{v0043.V0043JobInfoJobStateRUNNING},
								Nodes:    ptr.To(""),
							}},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelPlaceholderJobId: "3"}).Obj(),
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "Job not running",
			fields: fields{
				Client: func() client.Client {
					list := &slurmtypes.V0043JobInfoList{
						Items: []slurmtypes.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]v0043.V0043JobInfoJobState{v0043.V0043JobInfoJobStateCANCELLED},
								Nodes:    ptr.To(""),
							}},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelPlaceholderJobId: "1"}).Obj(),
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "Job found and running",
			fields: fields{
				Client: func() client.Client {
					list := &slurmtypes.V0043JobInfoList{
						Items: []slurmtypes.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
										Pods: []string{"slurm/foo"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]v0043.V0043JobInfoJobState{v0043.V0043JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							}},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelPlaceholderJobId: "1"}).Obj(),
			},
			want:    &PlaceholderJob{JobId: 1, Nodes: "node1"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client:    tt.fields.Client,
				partition: tt.fields.partition,
			}
			got, err := r.GetJob(tt.args.ctx, tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.GetSlurmJob() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetSlurmJob() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_SubmitJob(t *testing.T) {
	type fields struct {
		Client    client.Client
		partition string
	}
	type args struct {
		ctx        context.Context
		pod        *corev1.Pod
		slurmJobIR *slurmjobir.SlurmJobIR
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    int32
		wantErr bool
	}{
		{
			name: "Could not submit placeholder job",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, obj object.Object, req any, opts ...client.CreateOption) error {
							return fmt.Errorf("failed to create resource")
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx:        context.Background(),
				pod:        st.MakePod().Name("foo").Namespace("slurm-bridge").Obj(),
				slurmJobIR: &slurmjobir.SlurmJobIR{},
			},
			want:    0,
			wantErr: true,
		},
		{
			name: "Submit placeholder job",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, obj object.Object, req any, opts ...client.CreateOption) error {
							obj.(*slurmtypes.V0043JobInfo).JobId = ptr.To(int32(1))
							return nil
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx:        context.Background(),
				pod:        st.MakePod().Name("foo").Namespace("slurm-bridge").Obj(),
				slurmJobIR: &slurmjobir.SlurmJobIR{},
			},
			want:    1,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client:    tt.fields.Client,
				partition: tt.fields.partition,
			}
			got, err := r.SubmitJob(tt.args.ctx, tt.args.pod, tt.args.slurmJobIR)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.SubmitSlurmJob() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("realSlurmControl.SubmitSlurmJob() got= %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewControl(t *testing.T) {
	type args struct {
		client    client.Client
		mcsLabel  string
		partition string
	}
	tests := []struct {
		name string
		args args
		want SlurmControlInterface
	}{
		{
			name: "NewControl returns",
			args: args{
				client:    fake.NewFakeClient(),
				mcsLabel:  "kubernetes",
				partition: "slurm-bridge",
			},
			want: &realSlurmControl{
				Client:    fake.NewFakeClient(),
				mcsLabel:  "kubernetes",
				partition: "slurm-bridge",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewControl(tt.args.client, tt.args.mcsLabel, tt.args.partition); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewControl() = %v, want %v", got, tt.want)
			}
		})
	}
}
