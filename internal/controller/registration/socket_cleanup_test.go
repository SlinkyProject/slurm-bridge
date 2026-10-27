// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
			claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "partial-claim", Namespace: pod.Namespace, Labels: map[string]string{wellknown.LabelAllocationPodUID: string(pod.UID)}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}}}}
			foreign := claim.DeepCopy()
			foreign.Name = "different-owner"
			foreign.OwnerReferences[0].UID = "someone-else"
			c := fake.NewFakeClient(pod, claim, foreign)
			r := &Reconciler{Client: c}
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
