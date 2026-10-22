// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// staleCacheClient simulates a cached Get() that misses because the informer
// hasn't synced yet, while a SkipCache Get() reaches the real (found) object.
type staleCacheClient struct {
	client.Client
}

func (s *staleCacheClient) Get(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
	options := &client.GetOptions{}
	options.ApplyOptions(opts)
	if !options.SkipCache {
		return slurmerrors.ErrNotFound
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

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
							wellknown.LabelExternalJobId: "1",
						},
					},
				},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "Job not found in cache but found on live read",
			fields: fields{
				Client: func() client.Client {
					obj := &api.V0044JobInfo{
						JobId:    ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
					}
					return &staleCacheClient{Client: fake.NewClientBuilder().WithObjects(obj).Build()}
				}(),
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "Job not found in cache nor on live read",
			fields: fields{
				Client: &staleCacheClient{Client: fake.NewFakeClient()},
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
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
					obj := &api.V0044JobInfo{
						JobId:    ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
				},
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "Job found but canceled",
			fields: fields{
				Client: func() client.Client {
					obj := &api.V0044JobInfo{
						JobId: ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{
							api.V0044JobInfoJobStateCANCELLED,
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
							wellknown.LabelExternalJobId: "1",
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
					obj := &api.V0044JobInfo{
						JobId: ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{
							api.V0044JobInfoJobStateCOMPLETED,
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
							wellknown.LabelExternalJobId: "1",
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
					obj := &api.V0044JobInfo{
						JobId: ptr.To[int32](1),
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

func TestIsJobRunningVerifiesDestructiveResults(t *testing.T) {
	readErr := errors.New("Slurm unavailable")
	for _, tt := range []struct {
		name          string
		cached, live  api.V0044JobInfoJobState
		liveErr       error
		wrongID       bool
		want, wantErr bool
		wantLive      int
	}{
		{name: "cached running avoids live read", cached: api.V0044JobInfoJobStateRUNNING, liveErr: readErr, want: true},
		{name: "cached miss live running", live: api.V0044JobInfoJobStateRUNNING, want: true, wantLive: 1},
		{name: "cached completed live running", cached: api.V0044JobInfoJobStateCOMPLETED, live: api.V0044JobInfoJobStateRUNNING, want: true, wantLive: 1},
		{name: "confirmed completed", cached: api.V0044JobInfoJobStateCOMPLETED, live: api.V0044JobInfoJobStateCOMPLETED, wantLive: 1},
		{name: "confirmed absent", liveErr: slurmerrors.ErrNotFound, wantLive: 1},
		{name: "live error blocks cleanup", cached: api.V0044JobInfoJobStateCOMPLETED, liveErr: readErr, wantErr: true, wantLive: 1},
		{name: "wrong component blocks cleanup", cached: api.V0044JobInfoJobStateCOMPLETED, live: api.V0044JobInfoJobStateCOMPLETED, wrongID: true, wantErr: true, wantLive: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reads, liveReads := 0, 0
			cl := interceptor.NewClient(fake.NewFakeClient(), interceptor.Funcs{Get: func(_ context.Context, _ object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
				reads++
				options := new(client.GetOptions).ApplyOptions(opts)
				if options.RefreshCache {
					t.Error("liveness check requested RefreshCache")
				}
				state := tt.cached
				id := int32(7)
				if options.SkipCache {
					liveReads++
					if tt.liveErr != nil {
						return tt.liveErr
					}
					state = tt.live
					if tt.wrongID {
						id = 6
					}
				}
				if state == "" {
					return slurmerrors.ErrNotFound
				}
				*obj.(*api.V0044JobInfo) = api.V0044JobInfo{JobId: ptr.To(id), JobState: &[]api.V0044JobInfoJobState{state}}
				return nil
			}})
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{wellknown.LabelExternalJobId: "7"}}}
			got, err := NewControl(cl).IsJobRunning(t.Context(), pod)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("running = %t, %v; want %t, error %t", got, err, tt.want, tt.wantErr)
			}
			if reads != 1+tt.wantLive || liveReads != tt.wantLive {
				t.Fatalf("reads = %d, live = %d; want %d, %d", reads, liveReads, 1+tt.wantLive, tt.wantLive)
			}
		})
	}
}
