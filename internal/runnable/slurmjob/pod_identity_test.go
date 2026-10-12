// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	kubeinterceptor "sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
)

func TestSyncPodIdentity(t *testing.T) {
	for _, tt := range []struct {
		name       string
		comment    string
		state      api.V0044JobInfoJobState
		missing    bool
		lookupErr  bool
		wantDelete bool
	}{
		{
			name:       "recreated pod does not keep pending job alive",
			comment:    `{"pods":["default/work"],"podUIDs":{"default/work":"old-uid"}}`,
			state:      api.V0044JobInfoJobStatePENDING,
			wantDelete: true,
		},
		{
			name:       "recreated pod does not keep running job alive",
			comment:    `{"pods":["default/work"],"podUIDs":{"default/work":"old-uid"}}`,
			state:      api.V0044JobInfoJobStateRUNNING,
			wantDelete: true,
		},
		{
			name:    "matching UID keeps job alive",
			comment: `{"pods":["default/work"],"podUIDs":{"default/work":"current-uid"}}`,
			state:   api.V0044JobInfoJobStatePENDING,
		},
		{
			name:    "one matching member keeps gang alive",
			comment: `{"pods":["default/work","default/member"],"podUIDs":{"default/work":"old-uid","default/member":"member-uid"}}`,
			state:   api.V0044JobInfoJobStateRUNNING,
		},
		{
			name:       "all replaced members leave a dangling gang",
			comment:    `{"pods":["default/work","default/member"],"podUIDs":{"default/work":"old-uid","default/member":"old-member-uid"}}`,
			state:      api.V0044JobInfoJobStatePENDING,
			wantDelete: true,
		},
		{
			name:    "legacy comment retains name lookup",
			comment: `{"pods":["default/work"]}`,
			state:   api.V0044JobInfoJobStatePENDING,
		},
		{
			name:       "legacy dangling job is still removed",
			comment:    `{"pods":["default/work"]}`,
			state:      api.V0044JobInfoJobStatePENDING,
			missing:    true,
			wantDelete: true,
		},
		{
			name:       "missing UID-bearing pod is removed",
			comment:    `{"pods":["default/work"],"podUIDs":{"default/work":"current-uid"}}`,
			state:      api.V0044JobInfoJobStateRUNNING,
			missing:    true,
			wantDelete: true,
		},
		{
			name:    "finished job is not canceled again",
			comment: `{"pods":["default/work"],"podUIDs":{"default/work":"old-uid"}}`,
			state:   api.V0044JobInfoJobStateCOMPLETED,
		},
		{
			name:      "lookup error does not cancel job",
			comment:   `{"pods":["default/work"],"podUIDs":{"default/work":"old-uid"}}`,
			state:     api.V0044JobInfoJobStatePENDING,
			lookupErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pods := []client.Object{
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "member", UID: "member-uid"}},
			}
			if !tt.missing {
				pods = append(pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Namespace: "default", Name: "work", UID: "current-uid",
				}})
			}
			lookupErr := errors.New("pod lookup failed")
			kubeBuilder := kubefake.NewClientBuilder().WithObjects(pods...)
			if tt.lookupErr {
				kubeBuilder.WithInterceptorFuncs(kubeinterceptor.Funcs{
					Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
						return lookupErr
					},
				})
			}
			jobs := []object.Object{&api.V0044JobInfo{
				JobId: ptr.To(int32(7)), AdminComment: ptr.To(tt.comment), JobState: &[]api.V0044JobInfoJobState{tt.state},
			}}
			// Reproduce the issue with both the old and replacement jobs present.
			if !tt.missing {
				jobs = append(jobs, &api.V0044JobInfo{
					JobId:        ptr.To(int32(8)),
					AdminComment: ptr.To(`{"pods":["default/work"],"podUIDs":{"default/work":"current-uid"}}`),
					JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
				})
			}
			var deleted []int32
			slurm := fake.NewClientBuilder().WithObjects(jobs...).WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(_ context.Context, obj object.Object, _ ...slurmclient.DeleteOption) error {
					deleted = append(deleted, *obj.(*api.V0044JobInfo).JobId)
					return nil
				},
			}).Build()
			r := NewRunnable(kubeBuilder.Build(), slurm, make(chan event.GenericEvent, 10))
			err := r.Sync(t.Context())
			if tt.lookupErr {
				if !errors.Is(err, lookupErr) {
					t.Fatalf("Sync() error = %v, want lookup error", err)
				}
			} else if err != nil {
				t.Fatalf("Sync() error = %v", err)
			}
			var wantDeleted []int32
			if tt.wantDelete {
				wantDeleted = []int32{7}
			}
			if !slices.Equal(deleted, wantDeleted) {
				t.Errorf("canceled jobs = %v, want %v", deleted, wantDeleted)
			}
		})
	}
}
