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
)

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
