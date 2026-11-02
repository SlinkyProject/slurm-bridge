// SPDX-FileCopyrightText: Copyright (C) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	maxConcurrentReleases = 32
	releaseTimeout        = time.Minute
	// After this many failures the Pod goes back through scheduling, so a deleted
	// workload does not retry forever.
	maxReleaseAttempts = 5
	releaseEventBuffer = 1024
)

// releaseRetryDelay is the first retry backoff; it doubles up to a minute.
var releaseRetryDelay = time.Second

// releaseWork is the newest eligibility update for one allocation.
type releaseWork struct {
	logger   klog.Logger
	pod      *corev1.Pod
	state    stateData
	held     bool
	progress *groupProgress
	attempts int
}

// releaseHold queues the eligibility update and hold release for this allocation, so a
// slurmrestd round trip does not stall every pending Pod in the scheduling cycle.
func (sb *SlurmBridge) releaseHold(logger klog.Logger, pod *corev1.Pod, s *stateData, held bool, key groupAllocation, progress *groupProgress) {
	sb.releaseOnce.Do(sb.startReleaser)
	work := &releaseWork{logger: logger, pod: pod.DeepCopy(), state: *s, held: held, progress: progress}
	sb.mu.Lock()
	if sb.holdReleases == nil {
		sb.holdReleases = make(map[groupAllocation]*releaseWork)
	}
	if _, queued := sb.holdReleases[key]; !queued {
		sb.releases.Add(1)
	}
	sb.holdReleases[key] = work
	sb.mu.Unlock()
	sb.releaseEvents <- event.TypedGenericEvent[groupAllocation]{Object: key}
}

// startReleaser runs the release controller for the plugin's lifetime.
func (sb *SlurmBridge) startReleaser() {
	ctx := sb.releaseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	sb.releaseEvents = make(chan event.TypedGenericEvent[groupAllocation], releaseEventBuffer)
	c, err := controller.NewTypedUnmanaged("slurm-hold-release", controller.TypedOptions[groupAllocation]{
		Reconciler:              reconcile.TypedFunc[groupAllocation](sb.reconcileRelease),
		MaxConcurrentReconciles: maxConcurrentReleases,
		RateLimiter:             workqueue.NewTypedItemExponentialFailureRateLimiter[groupAllocation](releaseRetryDelay, time.Minute),
		// Every scheduler profile builds its own plugin.
		SkipNameValidation: ptr.To(true),
	})
	if err == nil {
		err = c.Watch(source.TypedChannel(sb.releaseEvents, handler.TypedFuncs[groupAllocation, groupAllocation]{
			GenericFunc: func(_ context.Context, e event.TypedGenericEvent[groupAllocation], q workqueue.TypedRateLimitingInterface[groupAllocation]) {
				q.Add(e.Object)
			},
		}))
	}
	if err != nil {
		// Options are static, so this only fails on a programming error.
		panic(err)
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			klog.FromContext(ctx).Error(err, "Slurm hold release controller stopped")
		}
	}()
}

func (sb *SlurmBridge) reconcileRelease(ctx context.Context, key groupAllocation) (reconcile.Result, error) {
	sb.mu.Lock()
	work := sb.holdReleases[key]
	delete(sb.holdReleases, key)
	sb.mu.Unlock()
	if work == nil {
		return reconcile.Result{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	status := sb.updateExternalJob(ctx, &work.state, work.held)
	work.attempts++
	switch {
	case status.IsSuccess():
		sb.finishRelease(key, work, true)
	case status.Code() == fwk.Unschedulable || work.attempts >= maxReleaseAttempts:
		// The allocation changed or Slurm keeps failing: schedule the Pod again from fresh state.
		work.logger.Info("Slurm hold release stopped; rescheduling", "pod", klog.KObj(work.pod), "attempts", work.attempts, "reason", status.Message())
		sb.finishRelease(key, work, false)
		sb.activatePod(work.logger, work.pod)
	default:
		sb.mu.Lock()
		_, newer := sb.holdReleases[key]
		if !newer {
			sb.holdReleases[key] = work
		}
		sb.mu.Unlock()
		if newer {
			sb.releases.Done()
		}
		work.logger.Info("Slurm hold release failed, retrying", "pod", klog.KObj(work.pod), "attempts", work.attempts, "reason", status.Message())
		return reconcile.Result{}, status.AsError()
	}
	return reconcile.Result{}, nil
}

// finishRelease lets the allocation's Pods trigger another update.
func (sb *SlurmBridge) finishRelease(key groupAllocation, work *releaseWork, released bool) {
	sb.mu.Lock()
	if work.progress != nil {
		work.progress.updating = false
	}
	if released {
		delete(sb.pending, key)
	}
	sb.mu.Unlock()
	sb.releases.Done()
}
