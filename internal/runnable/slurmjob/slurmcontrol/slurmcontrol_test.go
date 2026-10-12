// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
)

func Test_realSlurmControl_RefreshJobCache(t *testing.T) {
	tests := []struct {
		name        string
		slurmClient client.Client
		wantErr     bool
	}{
		{
			name:        "empty",
			slurmClient: fake.NewFakeClient(),
		},
		{
			name: "jobs",
			slurmClient: fake.NewClientBuilder().
				WithLists(&api.V0044JobInfoObjectList{
					Items: []api.V0044JobInfo{
						{

							JobId: ptr.To[int32](1),
						},
						{

							JobId: ptr.To[int32](2),
						},
					},
				}).
				Build(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewControl(tt.slurmClient)
			gotErr := r.RefreshJobCache(context.Background())
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("RefreshJobCache() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("RefreshJobCache() succeeded unexpectedly")
			}
		})
	}
}

func Test_realSlurmControl_ListPodsFromJobs(t *testing.T) {
	tests := []struct {
		name        string
		slurmClient client.Client
		wantJobIds  []int32
		wantPods    []kubetypes.NamespacedName
		wantErr     bool
	}{
		{
			name:        "empty",
			slurmClient: fake.NewFakeClient(),
			wantJobIds:  []int32{},
			wantPods:    []kubetypes.NamespacedName{},
		},
		{
			name: "running",
			slurmClient: fake.NewClientBuilder().
				WithLists(&api.V0044JobInfoObjectList{
					Items: []api.V0044JobInfo{
						{

							JobId: ptr.To[int32](1),
							JobState: ptr.To([]api.V0044JobInfoJobState{
								api.V0044JobInfoJobStateRUNNING,
							}),
						},
						{

							JobId: ptr.To[int32](2),
							JobState: ptr.To([]api.V0044JobInfoJobState{
								api.V0044JobInfoJobStateRUNNING,
							}),
							AdminComment: ptr.To("foo"),
						},
						{

							JobId: ptr.To[int32](3),
							JobState: ptr.To([]api.V0044JobInfoJobState{
								api.V0044JobInfoJobStateRUNNING,
							}),
							AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{
								Pods: []string{"foo/bar-0"},
							}).ToString()),
						},
						{

							JobId: ptr.To[int32](4),
							JobState: ptr.To([]api.V0044JobInfoJobState{
								api.V0044JobInfoJobStateRUNNING,
								api.V0044JobInfoJobStateCOMPLETING,
							}),
							AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{
								Pods: []string{"foo/bar-1"},
							}).ToString()),
						},
						{

							JobId: ptr.To[int32](5),
							JobState: ptr.To([]api.V0044JobInfoJobState{
								api.V0044JobInfoJobStateRUNNING,
								api.V0044JobInfoJobStateCOMPLETING,
							}),
							AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{
								Pods: []string{"foo/bar-2", "foo/bar-3"},
							}).ToString()),
						},
						{

							JobId: ptr.To[int32](6),
							JobState: ptr.To([]api.V0044JobInfoJobState{
								api.V0044JobInfoJobStatePENDING,
							}),
							AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{
								Pods: []string{"foo/bar-4"},
							}).ToString()),
						},
					},
				}).
				Build(),
			wantJobIds: []int32{3, 4, 5, 6},
			wantPods: []kubetypes.NamespacedName{
				{Namespace: "foo", Name: "bar-0"},
				{Namespace: "foo", Name: "bar-1"},
				{Namespace: "foo", Name: "bar-2"},
				{Namespace: "foo", Name: "bar-3"},
				{Namespace: "foo", Name: "bar-4"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewControl(tt.slurmClient)
			gotJobIds, gotPods, gotErr := r.ListPodsFromJobs(context.Background())
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("ListPodsFromJobs() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("ListPodsFromJobs() succeeded unexpectedly")
			}
			slices.Sort(gotJobIds)
			slices.Sort(tt.wantJobIds)
			if !apiequality.Semantic.DeepEqual(gotJobIds, tt.wantJobIds) {
				t.Errorf("ListPodsFromJobs() = %v, want %v", gotJobIds, tt.wantJobIds)
			}
			slices.SortFunc(gotPods, func(i, j kubetypes.NamespacedName) int {
				if i.String() < j.String() {
					return 1
				}
				return -1
			})
			slices.SortFunc(tt.wantPods, func(i, j kubetypes.NamespacedName) int {
				if i.String() < j.String() {
					return 1
				}
				return -1
			})
			if !apiequality.Semantic.DeepEqual(gotPods, tt.wantPods) {
				t.Errorf("ListPodsFromJobs() = %v, want %v", gotPods, tt.wantPods)
			}
		})
	}
}

func TestCompactPodReferences(t *testing.T) {
	control := NewControl(fake.NewClientBuilder().WithObjects(&api.V0044JobInfo{
		JobId:        ptr.To(int32(7)),
		AdminComment: ptr.To(`{"pods":"default/work/work-uid,default/member/member-uid"}`),
	}).Build())
	ids, names, err := control.ListPodsFromJobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []kubetypes.NamespacedName{
		{Namespace: "default", Name: "work"},
		{Namespace: "default", Name: "member"},
	}
	if !slices.Equal(ids, []int32{7}) || !slices.Equal(names, wantNames) {
		t.Errorf("job events = %v, %v; want job 7 and pod names %v", ids, names, wantNames)
	}
	refs, err := control.GetPodsFromJob(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	wantRefs := []corev1.ObjectReference{
		{Namespace: "default", Name: "work", UID: "work-uid"},
		{Namespace: "default", Name: "member", UID: "member-uid"},
	}
	if !slices.Equal(refs, wantRefs) {
		t.Errorf("cleanup references = %v, want %v", refs, wantRefs)
	}
}

func Test_realSlurmControl_GetPodsFromJob(t *testing.T) {
	tests := []struct {
		name        string
		slurmClient client.Client
		jobId       int32
		want        []corev1.ObjectReference
		wantErr     bool
	}{
		{
			name:        "not found",
			slurmClient: fake.NewFakeClient(),
			jobId:       0,
			want:        nil,
		},
		{
			name: "not ours",
			slurmClient: fake.NewClientBuilder().
				WithObjects(&api.V0044JobInfo{
					JobId: ptr.To[int32](1),
				}).
				Build(),
			jobId: 1,
			want:  []corev1.ObjectReference{},
		},
		{
			name: "ours",
			slurmClient: fake.NewClientBuilder().
				WithObjects(&api.V0044JobInfo{
					JobId: ptr.To[int32](1),
					AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{
						Pods: []string{"foo/bar-123"},
					}).ToString()),
				}).
				Build(),
			jobId: 1,
			want: []corev1.ObjectReference{
				{Namespace: "foo", Name: "bar-123"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewControl(tt.slurmClient)
			got, gotErr := r.GetPodsFromJob(context.Background(), tt.jobId)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("GetPodsFromJob() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("GetPodsFromJob() succeeded unexpectedly")
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("GetPodsFromJob() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_IsJobPendingOrRunning(t *testing.T) {
	ctx := context.Background()
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx   context.Context
		jobId int32
	}
	tests := []struct {
		name        string
		fields      fields
		args        args
		wantPending bool
		wantRunning bool
		wantErr     bool
	}{
		{
			name: "job not found",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx:   ctx,
				jobId: 1,
			},
			wantErr: false,
		},
		{
			name: "job pending",
			fields: fields{
				Client: func() client.Client {
					obj := &api.V0044JobInfo{
						JobId:    ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args:        args{ctx: ctx, jobId: 1},
			wantPending: true,
		},
		{
			name: "job running",
			fields: fields{
				Client: func() client.Client {
					obj := &api.V0044JobInfo{
						JobId:    ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args:        args{ctx: ctx, jobId: 1},
			wantRunning: true,
		},
		{
			name: "job pending and running (multiple states)",
			fields: fields{
				Client: func() client.Client {
					obj := &api.V0044JobInfo{
						JobId: ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{
							api.V0044JobInfoJobStatePENDING,
							api.V0044JobInfoJobStateRUNNING,
						},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args:        args{ctx: ctx, jobId: 1},
			wantPending: true,
			wantRunning: true,
		},
		{
			name: "job completed",
			fields: fields{
				Client: func() client.Client {
					obj := &api.V0044JobInfo{
						JobId:    ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCOMPLETED},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args: args{ctx: ctx, jobId: 1},
		},
		{
			name: "job canceled",
			fields: fields{
				Client: func() client.Client {
					obj := &api.V0044JobInfo{
						JobId:    ptr.To[int32](1),
						JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED},
					}
					return fake.NewClientBuilder().WithObjects(obj).Build()
				}(),
			},
			args: args{ctx: ctx, jobId: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			gotPending, gotRunning, err := r.IsJobPendingOrRunning(tt.args.ctx, tt.args.jobId)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.IsJobPendingOrRunning() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotPending != tt.wantPending || gotRunning != tt.wantRunning {
				t.Errorf("realSlurmControl.IsJobPendingOrRunning(%d) = (%t, %t), want (%t, %t)", tt.args.jobId, gotPending, gotRunning, tt.wantPending, tt.wantRunning)
			}
		})
	}
}

func Test_realSlurmControl_IsJobPendingOrRunningRefresh(t *testing.T) {
	failure := errors.New("get failed")
	for _, tt := range []struct {
		name        string
		cached      api.V0044JobInfoJobState
		live        api.V0044JobInfoJobState
		cachedErr   error
		liveErr     error
		wantPending bool
		wantRunning bool
		wantRefresh bool
		wantErr     error
	}{
		{name: "pending stays pending", cached: api.V0044JobInfoJobStatePENDING, live: api.V0044JobInfoJobStatePENDING, wantPending: true, wantRefresh: true},
		{name: "running stays running", cached: api.V0044JobInfoJobStateRUNNING, live: api.V0044JobInfoJobStateRUNNING, wantRunning: true, wantRefresh: true},
		{name: "pending becomes running", cached: api.V0044JobInfoJobStatePENDING, live: api.V0044JobInfoJobStateRUNNING, wantRunning: true, wantRefresh: true},
		{name: "running becomes pending", cached: api.V0044JobInfoJobStateRUNNING, live: api.V0044JobInfoJobStatePENDING, wantPending: true, wantRefresh: true},
		{name: "running completes", cached: api.V0044JobInfoJobStateRUNNING, live: api.V0044JobInfoJobStateCOMPLETED, wantRefresh: true},
		{name: "pending disappears", cached: api.V0044JobInfoJobStatePENDING, liveErr: slurmerrors.ErrNotFound, wantRefresh: true},
		{name: "refresh fails", cached: api.V0044JobInfoJobStateRUNNING, liveErr: failure, wantErr: failure, wantRefresh: true},
		{name: "completed skips refresh", cached: api.V0044JobInfoJobStateCOMPLETED, liveErr: failure},
		{name: "canceled skips refresh", cached: api.V0044JobInfoJobStateCANCELLED, liveErr: failure},
		{name: "unknown skips refresh", liveErr: failure},
		{name: "missing skips refresh", cachedErr: slurmerrors.ErrNotFound, liveErr: failure},
		{name: "cache error skips refresh", cachedErr: failure, liveErr: failure, wantErr: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			cl := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(_ context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
					options := &client.GetOptions{}
					options.ApplyOptions(opts)
					wantRefresh := calls > 0
					calls++
					if key != "1" || options.RefreshCache != wantRefresh {
						t.Errorf("Get(%s) RefreshCache = %t, want key 1 and RefreshCache %t", key, options.RefreshCache, wantRefresh)
					}
					state, err := tt.cached, tt.cachedErr
					if options.RefreshCache {
						state, err = tt.live, tt.liveErr
					}
					obj.(*api.V0044JobInfo).JobState = &[]api.V0044JobInfoJobState{state}
					return err
				},
			}).Build()
			r := &realSlurmControl{Client: cl}
			pending, running, err := r.IsJobPendingOrRunning(context.Background(), 1)
			if pending != tt.wantPending || running != tt.wantRunning || !errors.Is(err, tt.wantErr) {
				t.Errorf("IsJobPendingOrRunning(1) = (%t, %t, %v), want (%t, %t, %v)", pending, running, err, tt.wantPending, tt.wantRunning, tt.wantErr)
			}
			wantCalls := 1
			if tt.wantRefresh {
				wantCalls++
			}
			if calls != wantCalls {
				t.Errorf("IsJobPendingOrRunning(1) Get calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func Test_realSlurmControl_TerminateJob(t *testing.T) {
	ctx := context.Background()
	type args struct {
		ctx   context.Context
		jobId int32
	}
	tests := []struct {
		name        string
		slurmClient client.Client
		args        args
		wantErr     bool
	}{
		{
			name:        "Job not found",
			slurmClient: fake.NewFakeClient(),
			args: args{
				ctx:   ctx,
				jobId: 0,
			},
			wantErr: false,
		},
		{
			name: "Job deleted",
			slurmClient: func() client.Client {
				obj := &api.V0044JobInfo{
					JobId: ptr.To[int32](1),
				}
				return fake.NewClientBuilder().WithObjects(obj).Build()
			}(),
			args: args{
				ctx:   ctx,
				jobId: 1,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewControl(tt.slurmClient)
			if err := r.TerminateJob(tt.args.ctx, tt.args.jobId); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.TerminateJob() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
