// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	api "github.com/SlinkyProject/slurm-client/api/v0044"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
)

func TestAllocationKeys(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: "pod-uid"}}
	sibling := pod.DeepCopy()
	sibling.Name, sibling.UID = "sibling", "sibling-uid"
	for _, kind := range []string{"Pod", "PodGroup", "CompositePodGroup", "LeaderWorkerSet"} {
		t.Run(kind, func(t *testing.T) {
			root := metav1.PartialObjectMetadata{
				TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: kind},
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "group", UID: "group-uid"},
			}
			if kind == "LeaderWorkerSet" {
				root.APIVersion = lwsv1.GroupVersion.String()
			}
			key := allocationKey(pod, root)
			if same := key == allocationKey(sibling, root); same != (kind != "Pod") {
				t.Fatalf("sibling allocation shared=%t", same)
			}
			if kind == "Pod" {
				replacement := pod.DeepCopy()
				replacement.UID = "replacement"
				if key == allocationKey(replacement, root) {
					t.Fatal("recreated Pod reused allocation key")
				}
			} else {
				root.UID = "replacement"
				if key == allocationKey(pod, root) {
					t.Fatal("recreated group reused allocation key")
				}
			}
			if kind == "LeaderWorkerSet" {
				other := pod.DeepCopy()
				other.Labels = map[string]string{lwsv1.GroupUniqueHashLabelKey: "replica-2"}
				if allocationKey(pod, root) == allocationKey(other, root) {
					t.Fatal("LWS replicas share allocation key")
				}
			}
		})
	}
}

func TestJobEventRouting(t *testing.T) {
	leaf := externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "PodGroup", Namespace: "ns", Name: "leaf", UID: "leaf-uid"}
	root := externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "CompositePodGroup", Namespace: "ns", Name: "root", UID: "root-uid"}
	for _, tc := range []struct {
		name string
		info externaljobinfo.ExternalJobInfo
		want []Request
	}{
		{"legacy Pods", externaljobinfo.ExternalJobInfo{Pods: []string{"ns/pod/uid"}}, []Request{{Kind: "Pod", Namespace: "ns", Name: "pod", UID: "uid"}}},
		{"PodGroup", externaljobinfo.ExternalJobInfo{PodGroup: &leaf}, []Request{groupKey(leaf)}},
		{"composite root", externaljobinfo.ExternalJobInfo{PodGroup: &leaf, CompositePodGroup: &root}, []Request{groupKey(root)}},
		{"unowned", externaljobinfo.ExternalJobInfo{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []Request
			enqueueJob(&api.V0044JobInfo{AdminComment: ptr.To(tc.info.ToString())}, func(key Request) { got = append(got, key) })
			if !slices.Equal(got, tc.want) {
				t.Fatalf("queued %v, want %v", got, tc.want)
			}
		})
	}
}
