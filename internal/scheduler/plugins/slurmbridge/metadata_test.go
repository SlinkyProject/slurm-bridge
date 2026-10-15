// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type submissionGuardControl struct {
	slurmcontrol.SlurmControlInterface
	submits int
}

func (c *submissionGuardControl) SubmitJob(context.Context, *corev1.Pod, *slurmjobir.SlurmJobIR) ([]int32, error) {
	c.submits++
	return nil, errors.New("submission reached")
}

func TestSubmissionWaitsForControllerMetadataRepair(t *testing.T) {
	for _, tt := range []struct {
		name                                         string
		missing, finished, moved, liveErr, unlabeled bool
		correctlyLabeled, hetComponent               bool
		wantSubmits                                  int
		wantCode                                     fwk.Code
	}{
		{name: "existing allocation awaits repair", wantCode: fwk.Unschedulable},
		{name: "deleted candidate allows submission", missing: true, wantSubmits: 1, wantCode: fwk.Error},
		{name: "completed candidate allows submission", finished: true, wantSubmits: 1, wantCode: fwk.Error},
		{name: "changed ownership allows submission", moved: true, wantSubmits: 1, wantCode: fwk.Error},
		{name: "failed verification prevents submission", liveErr: true, wantCode: fwk.Error},
		{name: "unlabeled Pods retain submission behavior", unlabeled: true, wantSubmits: 1, wantCode: fwk.Error},
		{name: "correctly labeled siblings do not await repair", correctlyLabeled: true, wantSubmits: 1, wantCode: fwk.Error},
		{name: "correctly labeled hetjob component does not await repair", correctlyLabeled: true, hetComponent: true, wantSubmits: 1, wantCode: fwk.Error},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// An unlabeled triggering Pod must still wait for a labeled sibling.
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "new-pod"}}
			member := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "member", UID: "member-uid"}}
			if !tt.unlabeled {
				member.Labels = map[string]string{wellknown.LabelExternalJobId: "99"}
			}
			if tt.correctlyLabeled {
				member.Labels[wellknown.LabelExternalJobId] = "2"
			}
			if tt.hetComponent {
				member.Labels[wellknown.LabelExternalHetJobId] = "1"
			}
			before := member.DeepCopy()
			job := &api.V0044JobInfo{JobId: ptr.To(int32(2)), AdminComment: ptr.To(`{"pods":["ns/member/member-uid"]}`), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING}}
			if tt.hetComponent {
				job.HetJobId = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(1))}
				job.HetJobOffset = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(1))}
			}
			cache := fake.NewClientBuilder().WithObjects(job).Build()
			liveJob := job.DeepCopyObject().(*api.V0044JobInfo)
			if tt.finished {
				liveJob.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCOMPLETED}
			}
			if tt.moved {
				liveJob.AdminComment = ptr.To(`{"pods":["ns/other"]}`)
			}
			builder := fake.NewClientBuilder()
			if !tt.missing {
				builder.WithObjects(liveJob)
			}
			base := builder.Build()
			live := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
				if !(new(slurmclient.GetOptions).ApplyOptions(opts)).SkipCache {
					t.Error("submission verification did not bypass cache")
				}
				if tt.liveErr {
					return errors.New("Slurm unavailable")
				}
				return base.Get(ctx, key, obj, opts...)
			}})
			control, err := slurmcontrol.NewControl(live, cache.GetInformer(api.ObjectTypeV0044JobInfo), "", "")
			if err != nil {
				t.Fatal(err)
			}
			guard := &submissionGuardControl{SlurmControlInterface: control}
			sb := &SlurmBridge{slurmControl: guard}
			state := &stateData{slurmJobIR: &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{
				{Pods: corev1.PodList{Items: []corev1.Pod{*pod}}},
				{Pods: corev1.PodList{Items: []corev1.Pod{*member}}},
			}}}
			status := sb.submitExternalJob(t.Context(), state, pod)
			if status.Code() != tt.wantCode || guard.submits != tt.wantSubmits {
				t.Fatalf("status = %v, submissions = %d; want %v, %d", status, guard.submits, tt.wantCode, tt.wantSubmits)
			}
			if !reflect.DeepEqual(member, before) {
				t.Fatal("submission guard repaired metadata in the scheduler")
			}
		})
	}
}
