// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func TestLWSPolicyPreFilter(t *testing.T) {
	for _, tt := range []struct {
		restart lwsv1.RestartPolicyType
		startup lwsv1.StartupPolicyType
		size    int32
		want    fwk.Code
	}{
		{restart: lwsv1.RecreateGroupOnPodRestart, size: 2, want: fwk.Success},
		{restart: lwsv1.NoneRestartPolicy, size: 2, want: fwk.UnschedulableAndUnresolvable},
		{restart: lwsv1.RecreateGroupAfterStart, size: 2, want: fwk.UnschedulableAndUnresolvable},
		{restart: lwsv1.RecreateGroupOnPodRestart, startup: lwsv1.LeaderReadyStartupPolicy, size: 2, want: fwk.UnschedulableAndUnresolvable},
		{restart: lwsv1.RecreateGroupOnPodRestart, startup: lwsv1.LeaderReadyStartupPolicy, size: 1, want: fwk.Success},
	} {
		t.Run(fmt.Sprintf("restart=%q startup=%q size=%d", tt.restart, tt.startup, tt.size), func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(lwsv1.AddToScheme(scheme))
			lws := newLWS("lws", tt.size)
			lws.Spec.StartupPolicy = tt.startup
			lws.Spec.LeaderWorkerTemplate.RestartPolicy = tt.restart
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(lws).Build()
			tr := &translator{Reader: c, ctx: t.Context()}
			// Enough pods that only the policy checks can reject the group.
			pods := make([]corev1.Pod, tt.size)
			ir := &SlurmJobIR{
				RootPOM:    metav1.PartialObjectMetadata{ObjectMeta: lws.ObjectMeta},
				Components: []SlurmJobComponent{{Pods: corev1.PodList{Items: pods}}},
			}
			if got := tr.PreFilterLWS(&corev1.Pod{}, ir); got.Code() != tt.want {
				t.Errorf("PreFilterLWS() = %v, want code %v", got, tt.want)
			}
		})
	}
}
