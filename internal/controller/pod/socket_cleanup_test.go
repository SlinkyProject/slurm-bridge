// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/pod/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestPartialCPUClaimCleanup(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "terminal"}[terminal], func(t *testing.T) {
			ctx := context.Background()
			pod := newQueuedPod("partial", 1)
			pod.UID = types.UID("pod-uid")
			pod.Finalizers = []string{wellknown.FinalizerScheduler}
			if terminal {
				pod.Status.Phase = corev1.PodFailed
			}
			claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "partial-claim", Namespace: pod.Namespace, Labels: map[string]string{"slinky.slurm.net/allocation-pod-uid": string(pod.UID)}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}}}}
			foreign := claim.DeepCopy()
			foreign.Name = "different-owner"
			foreign.OwnerReferences[0].UID = "someone-else"
			c := fake.NewFakeClient(pod, claim, foreign)
			r := &PodReconciler{Client: c}
			if err := r.prepareTerminalPod(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(pod)}); err != nil {
				t.Fatal(err)
			}
			err := c.Get(ctx, client.ObjectKeyFromObject(claim), &resourcev1.ResourceClaim{})
			if apierrors.IsNotFound(err) != terminal {
				t.Fatalf("claim cleanup terminal=%v: %v", terminal, err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(foreign), &resourcev1.ResourceClaim{}); err != nil {
				t.Fatalf("deleted another owner's claim: %v", err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
				t.Fatal(err)
			}
			if (len(pod.Finalizers) == 0) != terminal {
				t.Fatalf("unexpected finalizers %v", pod.Finalizers)
			}
		})
	}
}

type failingSocketCleanup struct {
	slurmcontrol.SlurmControlInterface
	fail bool
}

func (c *failingSocketCleanup) IsJobPendingOrRunning(context.Context, int32) (bool, error) {
	return true, nil
}
func (c *failingSocketCleanup) TerminateJob(context.Context, int32) error {
	if c.fail {
		return errors.New("slurm cancellation unavailable")
	}
	return nil
}

func TestSocketCleanupRetainsOwnershipUntilSlurmAcceptsCancellation(t *testing.T) {
	for _, deletingUnbound := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal", true: "deleting-unbound"}[deletingUnbound], func(t *testing.T) {

			ctx := context.Background()
			pod := newQueuedPod("retry-cleanup", 1)
			pod.UID = "cleanup-pod"
			pod.Finalizers = []string{wellknown.FinalizerScheduler}
			if deletingUnbound {
				pod.DeletionTimestamp = ptr.To(metav1.Now())
			} else {
				pod.Status.Phase = corev1.PodFailed
			}
			claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "partial", Namespace: pod.Namespace, Labels: map[string]string{"slinky.slurm.net/allocation-pod-uid": string(pod.UID)}, OwnerReferences: []metav1.OwnerReference{{Kind: "Pod", Name: pod.Name, UID: pod.UID}}}}
			c := fake.NewFakeClient(pod, claim)
			control := &failingSocketCleanup{fail: true}
			r := &PodReconciler{Client: c, slurmControl: control}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
			if err := r.Sync(ctx, req); err == nil {
				t.Fatal("expected cancellation error")
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(claim), &resourcev1.ResourceClaim{}); err != nil {
				t.Fatalf("lost claim while Slurm cleanup failed: %v", err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
				t.Fatal(err)
			}
			if len(pod.Finalizers) != 1 {
				t.Fatal("lost finalizer while Slurm cleanup failed")
			}
			control.fail = false
			if err := r.Sync(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(claim), &resourcev1.ResourceClaim{}); !apierrors.IsNotFound(err) {
				t.Fatalf("claim was not cleaned up: %v", err)
			}

		})
	}
}
