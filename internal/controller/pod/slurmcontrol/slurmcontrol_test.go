// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	v0044 "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/types"
)

func Test_realSlurmControl_GetJob(t *testing.T) {
	ctx := context.Background()
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "Job not found",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "Job found",
			fields: fields{
				Client: func() client.Client {
					obj := &types.V0044JobInfo{
						V0044JobInfo: v0044.V0044JobInfo{
							JobId:    ptr.To[int32](1),
							JobState: &[]v0044.V0044JobInfoJobState{v0044.V0044JobInfoJobStateRUNNING},
						},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "Job found but cancelled",
			fields: fields{
				Client: func() client.Client {
					obj := &types.V0044JobInfo{
						V0044JobInfo: v0044.V0044JobInfo{
							JobId: ptr.To[int32](1),
							JobState: &[]v0044.V0044JobInfoJobState{
								v0044.V0044JobInfoJobStateCANCELLED,
							},
						},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "Job found but completed",
			fields: fields{
				Client: func() client.Client {
					obj := &types.V0044JobInfo{
						V0044JobInfo: v0044.V0044JobInfo{
							JobId: ptr.To[int32](1),
							JobState: &[]v0044.V0044JobInfoJobState{
								v0044.V0044JobInfoJobStateCOMPLETED,
							},
						},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
			},
			want:    false,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			got, err := r.IsJobRunning(tt.args.ctx, tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.IsJobRunning() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("realSlurmControl.IsJobRunning() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_TerminateJob(t *testing.T) {
	ctx := context.Background()
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx   context.Context
		jobId int32
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "Job not found",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx:   ctx,
				jobId: 0,
			},
			wantErr: false,
		},
		{
			name: "Job deleted",
			fields: fields{
				Client: func() client.Client {
					obj := &types.V0044JobInfo{
						V0044JobInfo: v0044.V0044JobInfo{
							JobId: ptr.To[int32](1),
						},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args: args{
				ctx:   ctx,
				jobId: 1,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			if err := r.TerminateJob(tt.args.ctx, tt.args.jobId); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.TerminateJob() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_tolerateError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Nil",
			args: args{
				err: nil,
			},
			want: true,
		},
		{
			name: "Empty",
			args: args{
				err: errors.New(""),
			},
			want: false,
		},
		{
			name: "NotFound",
			args: args{
				err: errors.New(http.StatusText(http.StatusNotFound)),
			},
			want: true,
		},
		{
			name: "NoContent",
			args: args{
				err: errors.New(http.StatusText(http.StatusNoContent)),
			},
			want: true,
		},
		{
			name: "Forbidden",
			args: args{
				err: errors.New(http.StatusText(http.StatusForbidden)),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tolerateError(tt.args.err); got != tt.want {
				t.Errorf("tolerateError() = %v, want %v", got, tt.want)
			}
		})
	}
}
