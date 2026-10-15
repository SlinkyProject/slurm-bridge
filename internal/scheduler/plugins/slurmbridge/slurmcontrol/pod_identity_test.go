// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
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
		if !slices.Equal(info.Pods, []string{"default/work/work-uid", "default/member/member-uid"}) {
			t.Errorf("update=%t: pod identities = %v", update, info.Pods)
		}
		if want := `{"pods":"default/work/work-uid,default/member/member-uid"}`; ptr.Deref(desc.AdminComment, "") != want {
			t.Errorf("update=%t: admin comment = %s, want %s", update, ptr.Deref(desc.AdminComment, ""), want)
		}
	}
}

func TestJobsForPodIdentity(t *testing.T) {
	r := &realSlurmControl{Client: fake.NewClientBuilder().WithObjects(
		&api.V0044JobInfo{
			JobId:        ptr.To(int32(6)),
			AdminComment: ptr.To(`{"pods":["default/work"]}`),
		},
		&api.V0044JobInfo{
			JobId:        ptr.To(int32(7)),
			AdminComment: ptr.To(`{"pods":"default/work/old-uid"}`),
		},
		&api.V0044JobInfo{
			JobId:        ptr.To(int32(8)),
			AdminComment: ptr.To(`{"pods":"default/work/new-uid"}`),
		},
		&api.V0044JobInfo{
			JobId:        ptr.To(int32(9)),
			AdminComment: ptr.To(`{"pods":["default/work"],"podUIDs":{"default/work":"map-uid"}}`),
		},
	).Build()}
	control, err := NewControl(r.Client, r.GetInformer(api.ObjectTypeV0044JobInfo), "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		uid  kubetypes.UID
		want int32
	}{
		{uid: "old-uid", want: 7},
		{uid: "new-uid", want: 8},
		{uid: "legacy-uid", want: 6},
		{uid: "map-uid", want: 9},
	} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "work", UID: tt.uid}}
		jobs, err := control.GetJobsForPods(t.Context(), pod)
		if err != nil {
			t.Fatal(err)
		}
		if job, found := JobForPod(*jobs, pod); !found || job.JobId != tt.want {
			t.Errorf("job for UID %s = (%d, %t), want %d", tt.uid, job.JobId, found, tt.want)
		}
	}
}
