// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
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
			r := NewRunnable(kube, slurm, make(chan event.GenericEvent, 10))
			if err := r.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(deleted, tt.wantDeleted) {
				t.Errorf("canceled jobs = %v, want %v", deleted, tt.wantDeleted)
			}
		})
	}
}
