// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestSlurmBridge_HetJobOffsetLabel(t *testing.T) {
	for _, tt := range []struct {
		name   string
		leader int32
		offset int32
		want   string
	}{
		{name: "leader component", leader: 93, offset: 0, want: "0"},
		{name: "non-leader component", leader: 93, offset: 2, want: "2"},
		{name: "homogeneous job removes stale offset"},
	} {
		for _, refresh := range []bool{false, true} {
			name := tt.name + "/submission"
			if refresh {
				name = tt.name + "/refresh"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "workload", Name: "pod", Labels: map[string]string{
					wellknown.LabelExternalJobId:        "95",
					wellknown.LabelExternalHetJobOffset: "9",
				}}}
				cl := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy()).Build()
				sb := &SlurmBridge{Client: cl, slurmControl: &postFilterSlurmControl{podToJobs: map[string]slurmcontrol.ExternalJob{
					kubeclient.ObjectKeyFromObject(pod).String(): {JobId: 95, HetJobId: tt.leader, HetJobOffset: tt.offset},
				}}}
				var err error
				if refresh {
					_, err = sb.validatePodToJob(ctx, pod)
				} else {
					err = sb.labelPodsWithJobId(ctx, 95, tt.leader, tt.offset, slurmjobir.SlurmJobComponent{Pods: corev1.PodList{Items: []corev1.Pod{*pod}}})
				}
				if err != nil {
					t.Fatal(err)
				}
				got := &corev1.Pod{}
				if err := cl.Get(ctx, kubeclient.ObjectKeyFromObject(pod), got); err != nil {
					t.Fatal(err)
				}
				value, present := got.Labels[wellknown.LabelExternalHetJobOffset]
				if value != tt.want || present != (tt.leader > 0) {
					t.Errorf("offset label = %q (present %t), want %q (present %t)", value, present, tt.want, tt.leader > 0)
				}
			})
		}
	}
}
