// SPDX-FileCopyrightText: Copyright (C) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func init() {
	// Retry failed releases without waiting, so failure cases finish quickly.
	releaseRetryDelay = 0
}

// flakyUpdateControl fails the first updates, as an overloaded slurmrestd would.
type flakyUpdateControl struct {
	compositeUpdateRecorder
	failures atomic.Int32
	calls    atomic.Int32
}

func (c *flakyUpdateControl) UpdateJob(ctx context.Context, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) (int32, error) {
	if c.calls.Add(1) <= c.failures.Load() {
		return 0, errors.New("slurmrestd unavailable")
	}
	return c.compositeUpdateRecorder.UpdateJob(ctx, pod, ir)
}

func TestReleaseHoldRetriesTransientFailures(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: "uid",
		Labels: map[string]string{wellknown.LabelExternalJobId: "7"}}}
	ir := &slurmjobir.SlurmJobIR{
		RootPOM:    metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "root", UID: "uid"}},
		Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{pod}}}},
	}
	control := &flakyUpdateControl{}
	control.failures.Store(maxReleaseAttempts - 1)
	sb := &SlurmBridge{slurmControl: control, releaseCtx: t.Context()}
	key := groupAllocation{uid: "uid", jobID: 7}
	sb.pending = map[groupAllocation]*groupProgress{key: {updating: true}}

	sb.releaseHold(klog.Background(), &pod, &stateData{slurmJobIR: ir, allocation: testAllocation(ir)}, true, key, sb.pending[key])
	sb.releases.Wait()

	if got := control.calls.Load(); got != maxReleaseAttempts {
		t.Errorf("UpdateJob calls = %d, want %d", got, maxReleaseAttempts)
	}
	if len(control.roots) != 1 {
		t.Errorf("successful updates = %v, want one", control.roots)
	}
	if _, ok := sb.pending[key]; ok {
		t.Error("released allocation is still pending")
	}
}
