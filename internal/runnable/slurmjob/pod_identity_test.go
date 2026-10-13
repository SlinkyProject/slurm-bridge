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
	kubetypes "k8s.io/apimachinery/pkg/types"
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

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
)

func TestSyncPodIdentity(t *testing.T) {
	for _, tt := range []struct {
		name        string
		comment     string
		wantDeleted []int32
	}{
		{
			name:        "recreated pod leaves old job dangling",
			comment:     `{"pods":["default/work"],"podUIDs":{"default/work":"old-uid"}}`,
			wantDeleted: []int32{7},
		},
		{
			name:    "matching UID keeps job alive",
			comment: `{"pods":["default/work"],"podUIDs":{"default/work":"current-uid"}}`,
		},
		{
			name:    "one matching member keeps gang alive",
			comment: `{"pods":["default/work","default/member"],"podUIDs":{"default/work":"old-uid","default/member":"member-uid"}}`,
		},
		{
			name:    "legacy comment retains name lookup",
			comment: `{"pods":["default/work"]}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kube := kubefake.NewClientBuilder().WithObjects(
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "work", UID: "current-uid"}},
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "member", UID: "member-uid"}},
			).Build()
			var deleted []int32
			slurm := fake.NewClientBuilder().WithObjects(
				&api.V0044JobInfo{
					JobId: ptr.To(int32(7)), AdminComment: ptr.To(tt.comment),
					JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
				},
				&api.V0044JobInfo{
					JobId:        ptr.To(int32(8)),
					AdminComment: ptr.To(`{"pods":["default/work"],"podUIDs":{"default/work":"current-uid"}}`),
					JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
				},
			).WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(_ context.Context, obj object.Object, _ ...slurmclient.DeleteOption) error {
					deleted = append(deleted, *obj.(*api.V0044JobInfo).JobId)
					return nil
				},
			}).Build()
			r := NewRunnable(kube, kube, slurm, make(chan event.GenericEvent, 10))
			if err := r.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(deleted, tt.wantDeleted) {
				t.Errorf("canceled jobs = %v, want %v", deleted, tt.wantDeleted)
			}
		})
	}
}

func TestCleanDanglingJobConfirmsPodIdentity(t *testing.T) {
	lookupErr := errors.New("API unavailable")
	for _, tt := range []struct {
		name          string
		jobUID        kubetypes.UID
		cachedUID     kubetypes.UID
		liveUID       kubetypes.UID
		liveErr       error
		wantDeleted   bool
		wantLiveReads int
	}{
		{
			name:   "matching cached UID avoids live lookup",
			jobUID: "current-uid", cachedUID: "current-uid", liveErr: lookupErr,
		},
		{
			name:   "old cached UID does not cancel replacement job",
			jobUID: "current-uid", cachedUID: "old-uid", liveUID: "current-uid", wantLiveReads: 1,
		},
		{
			name:   "cache miss does not cancel existing pod job",
			jobUID: "current-uid", liveUID: "current-uid", wantLiveReads: 1,
		},
		{
			name:   "confirmed UID mismatch cancels old job",
			jobUID: "old-uid", cachedUID: "current-uid", liveUID: "current-uid", wantDeleted: true, wantLiveReads: 1,
		},
		{
			name:   "confirmed deletion cancels job",
			jobUID: "old-uid", wantDeleted: true, wantLiveReads: 1,
		},
		{
			name:   "live lookup failure retains job",
			jobUID: "current-uid", cachedUID: "old-uid", liveErr: lookupErr, wantLiveReads: 1,
		},
		{
			name:      "legacy job retains cached name matching",
			cachedUID: "current-uid", liveErr: lookupErr,
		},
		{
			name:    "legacy job retains live name matching",
			liveUID: "current-uid", wantLiveReads: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objects := func(uid kubetypes.UID) []client.Object {
				if uid == "" {
					return nil
				}
				return []client.Object{&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Namespace: "default", Name: "work", UID: uid,
				}}}
			}
			kube := kubefake.NewClientBuilder().WithObjects(objects(tt.cachedUID)...).Build()
			liveReads := 0
			apiReader := kubefake.NewClientBuilder().WithObjects(objects(tt.liveUID)...).
				WithInterceptorFuncs(kubeinterceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						liveReads++
						if tt.liveErr != nil {
							return tt.liveErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).Build()
			info := externaljobinfo.ExternalJobInfo{Pods: []string{"default/work"}}
			if tt.jobUID != "" {
				info.PodUIDs = map[string]kubetypes.UID{"default/work": tt.jobUID}
			}
			deleted := false
			slurm := fake.NewClientBuilder().WithObjects(&api.V0044JobInfo{
				JobId: ptr.To(int32(7)), AdminComment: ptr.To(info.ToString()),
				JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
			}).WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(_ context.Context, _ object.Object, _ ...slurmclient.DeleteOption) error {
					deleted = true
					return nil
				},
			}).Build()
			r := NewRunnable(kube, apiReader, slurm, make(chan event.GenericEvent, 10))
			err := r.Sync(t.Context())
			var wantErr error
			if tt.wantLiveReads > 0 {
				wantErr = tt.liveErr
			}
			if !errors.Is(err, wantErr) {
				t.Errorf("Sync() error = %v, want %v", err, wantErr)
			}
			if deleted != tt.wantDeleted {
				t.Errorf("canceled job = %t, want %t", deleted, tt.wantDeleted)
			}
			if liveReads != tt.wantLiveReads {
				t.Errorf("live reads = %d, want %d", liveReads, tt.wantLiveReads)
			}
		})
	}
}
