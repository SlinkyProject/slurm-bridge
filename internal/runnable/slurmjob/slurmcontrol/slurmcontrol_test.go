// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
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
				WithLists(&types.V0044JobInfoList{
					Items: []types.V0044JobInfo{
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](1),
							},
						},
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](2),
							},
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
				WithLists(&types.V0044JobInfoList{
					Items: []types.V0044JobInfo{
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](1),
								JobState: ptr.To([]api.V0044JobInfoJobState{
									api.V0044JobInfoJobStateRUNNING,
								}),
							},
						},
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](2),
								JobState: ptr.To([]api.V0044JobInfoJobState{
									api.V0044JobInfoJobStateRUNNING,
								}),
								AdminComment: ptr.To("foo"),
							},
						},
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](3),
								JobState: ptr.To([]api.V0044JobInfoJobState{
									api.V0044JobInfoJobStateRUNNING,
								}),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"foo/bar-0"},
								}).ToString()),
							},
						},
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](4),
								JobState: ptr.To([]api.V0044JobInfoJobState{
									api.V0044JobInfoJobStateRUNNING,
									api.V0044JobInfoJobStateCOMPLETING,
								}),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"foo/bar-1"},
								}).ToString()),
							},
						},
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](5),
								JobState: ptr.To([]api.V0044JobInfoJobState{
									api.V0044JobInfoJobStateRUNNING,
									api.V0044JobInfoJobStateCOMPLETING,
								}),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"foo/bar-2", "foo/bar-3"},
								}).ToString()),
							},
						},
						{
							V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](6),
								JobState: ptr.To([]api.V0044JobInfoJobState{
									api.V0044JobInfoJobStatePENDING,
								}),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"foo/bar-4"},
								}).ToString()),
							},
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

func Test_realSlurmControl_GetPodsFromJob(t *testing.T) {
	tests := []struct {
		name        string
		slurmClient client.Client
		jobId       int32
		want        []kubetypes.NamespacedName
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
				WithObjects(&types.V0044JobInfo{
					V0044JobInfo: api.V0044JobInfo{
						JobId: ptr.To[int32](1),
					},
				}).
				Build(),
			jobId: 1,
			want:  []kubetypes.NamespacedName{},
		},
		{
			name: "ours",
			slurmClient: fake.NewClientBuilder().
				WithObjects(&types.V0044JobInfo{
					V0044JobInfo: api.V0044JobInfo{
						JobId: ptr.To[int32](1),
						AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
							Pods: []string{"foo/bar-123"},
						}).ToString()),
					},
				}).
				Build(),
			jobId: 1,
			want: []kubetypes.NamespacedName{
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
				obj := &types.V0044JobInfo{
					V0044JobInfo: api.V0044JobInfo{
						JobId: ptr.To[int32](1),
					},
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
