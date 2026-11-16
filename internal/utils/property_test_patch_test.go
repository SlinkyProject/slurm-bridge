// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFieldPatchProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default", UID: "live"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if rapid.Bool().Draw(rt, "existing-field") {
			pod.Status.Message = "previous"
		}
		before := pod.DeepCopy()
		c := fake.NewClientBuilder().WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
		value := rapid.String().Draw(rt, "value")
		uid := rapid.SampledFrom([]types.UID{"live", "stale", ""}).Draw(rt, "uid")
		guarded := rapid.Bool().Draw(rt, "uid-precondition")
		var opts []FieldPatchOption
		if guarded {
			opts = append(opts, WithUIDPrecondition(uid))
		}
		patch, err := FieldPatch("/status/message", value, opts...)
		if guarded && uid == "" {
			if err == nil {
				rt.Fatal("accepted an empty UID precondition")
			}
			return
		}
		if err != nil {
			rt.Fatal(err)
		}
		err = c.Status().Patch(rt.Context(), pod, patch)
		rejected := guarded && uid != before.UID
		if (err != nil) != rejected {
			rt.Fatalf("patch error=%v, want rejected=%t", err, rejected)
		}
		stored := &corev1.Pod{}
		if err := c.Get(rt.Context(), client.ObjectKeyFromObject(before), stored); err != nil {
			rt.Fatal(err)
		}
		want := value
		if rejected {
			want = before.Status.Message
		}
		if stored.Status.Message != want || stored.Status.Phase != before.Status.Phase || stored.UID != before.UID {
			rt.Fatal("patch changed the wrong fields or applied despite a failed precondition")
		}
	})
}
