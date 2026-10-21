// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/ktesting"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestPodWithMetadataPlacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		job      ExternalJob
		bound    bool
		wantNode string
	}{
		{name: "pending cache record preserves placement", job: ExternalJob{Pending: true}, wantNode: "node1"},
		{name: "running allocation contains placement", job: ExternalJob{Nodes: "node[1-2]"}, wantNode: "node1"},
		{name: "running allocation excludes placement", job: ExternalJob{Nodes: "node2"}},
		{name: "finished allocation clears placement", job: ExternalJob{Finished: true, Nodes: "node1"}},
		{name: "bound pod preserves placement", job: ExternalJob{Finished: true}, bound: true, wantNode: "node1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node1"},
			}}
			if tc.bound {
				pod.Spec.NodeName = "node1"
			}
			updated := PodWithMetadata(t.Context(), pod, tc.job, false)
			if got := updated.Annotations[wellknown.AnnotationExternalJobNode]; got != tc.wantNode {
				t.Errorf("node annotation = %q, want %q", got, tc.wantNode)
			}
		})
	}
}

func Test_validateIDLabel(t *testing.T) {
	const label = "test/job-id"
	tests := []struct {
		name       string
		id         int32
		labels     map[string]string
		wantLabels map[string]string
		wantDelete bool
	}{
		{name: "set missing label", id: 5, labels: map[string]string{}, wantLabels: map[string]string{label: "5"}},
		{name: "delete stale label", id: 0, labels: map[string]string{label: "7"}, wantLabels: map[string]string{}, wantDelete: true},
		{name: "delete present empty-value label", id: 0, labels: map[string]string{label: ""}, wantLabels: map[string]string{}, wantDelete: true},
		{name: "no label, no delete log", id: 0, labels: map[string]string{}, wantLabels: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := ktesting.NewLogger(t, ktesting.NewConfig(ktesting.Verbosity(3), ktesting.BufferLogs(true)))
			ctx := klog.NewContext(context.Background(), logger)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: tt.labels}}
			newPod := pod.DeepCopy()
			validateIDLabel(ctx, tt.id, label, pod, newPod)
			if !reflect.DeepEqual(newPod.Labels, tt.wantLabels) {
				t.Errorf("labels = %v, want %v", newPod.Labels, tt.wantLabels)
			}
			logs := logger.GetSink().(ktesting.Underlier).GetBuffer().String()
			if got := strings.Contains(logs, "Deleting invalid label"); got != tt.wantDelete {
				t.Errorf("logged delete = %v, want %v; logs:\n%s", got, tt.wantDelete, logs)
			}
		})
	}
}
