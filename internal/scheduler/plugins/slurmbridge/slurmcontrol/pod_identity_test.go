// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"maps"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestBuildJobDescPodIdentity(t *testing.T) {
	component := slurmjobir.SlurmJobComponent{Pods: corev1.PodList{Items: []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "work", UID: "work-uid"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "member", UID: "member-uid"}},
	}}}
	for _, update := range []bool{false, true} {
		desc, err := (&realSlurmControl{}).buildJobDesc(component, update)
		if err != nil {
			t.Fatal(err)
		}
		var info externaljobinfo.ExternalJobInfo
		if err := externaljobinfo.ParseIntoExternalJobInfo(desc.AdminComment, &info); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(info.Pods, []string{"default/work", "default/member"}) {
			t.Errorf("update=%t: pod names = %v", update, info.Pods)
		}
		wantUIDs := map[string]kubetypes.UID{"default/work": "work-uid", "default/member": "member-uid"}
		if !maps.Equal(info.PodUIDs, wantUIDs) {
			t.Errorf("update=%t: pod UIDs = %v, want %v", update, info.PodUIDs, wantUIDs)
		}
	}
}

func TestJobsForPodIdentity(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		jobs := []api.V0044JobInfo{
			{
				JobId:        ptr.To(int32(7)),
				AdminComment: ptr.To(`{"pods":["default/work"],"podUIDs":{"default/work":"old-uid"}}`),
			},
			{
				JobId:        ptr.To(int32(8)),
				AdminComment: ptr.To(`{"pods":["default/work","default/member"],"podUIDs":{"default/work":"new-uid","default/member":"member-uid"}}`),
			},
		}
		if legacy {
			jobs = append(jobs, api.V0044JobInfo{
				JobId:        ptr.To(int32(6)),
				AdminComment: ptr.To(`{"pods":["default/work"]}`),
			})
		}
		r := &realSlurmControl{Client: fake.NewClientBuilder().WithLists(&api.V0044JobInfoObjectList{Items: jobs}).Build()}
		podToJob, err := r.GetJobsForPods(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			name string
			uid  kubetypes.UID
			want int32
		}{
			{name: "work", uid: "old-uid", want: 7},
			{name: "work", uid: "new-uid", want: 8},
			{name: "member", uid: "member-uid", want: 8},
			{name: "work", uid: "unrelated-uid"},
		} {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: tt.name, UID: tt.uid}}
			want := tt.want
			if legacy && want == 0 {
				want = 6
			}
			job, found := JobForPod(*podToJob, pod)
			if found != (want != 0) || job.JobId != want {
				t.Errorf("legacy=%t: job for %s UID %s = (%d, %t), want %d", legacy, tt.name, tt.uid, job.JobId, found, want)
			}
		}
	}
}
