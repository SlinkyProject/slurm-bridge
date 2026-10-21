// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestLegacyAllocationMembership(t *testing.T) {
	ir := &slurmjobir.SlurmJobIR{
		RootPOM: metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "PodGroup"}},
		Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "first", UID: "first-uid"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "second", UID: "second-uid"}},
		}}}},
	}
	for _, tc := range []struct {
		name     string
		pods     []string
		complete bool
	}{
		{"UID membership", []string{"ns/first/first-uid", "ns/second/second-uid"}, true},
		{"legacy names", []string{"ns/first", "ns/second"}, true},
		{"partial group", []string{"ns/first/first-uid"}, false},
		{"replaced member", []string{"ns/first/first-uid", "ns/second/old-uid"}, false},
		{"different namespace", []string{"other/first", "other/second"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := []RegisteredJob{{ExternalJob: ExternalJob{JobId: 100}, info: externaljobinfo.ExternalJobInfo{Pods: tc.pods}}}
			got, complete, err := MatchAllocationComponents(ir, jobs)
			if err != nil || complete != tc.complete {
				t.Fatalf("match=%v, complete=%t, error=%v", got, complete, err)
			}
			if complete && (len(got) != 1 || got[0].JobId != 100) {
				t.Fatalf("wrong allocation: %v", got)
			}
		})
	}
}

func TestAllocationIndexKeys(t *testing.T) {
	leaf := externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "PodGroup", Namespace: "ns", Name: "leaf", UID: "leaf-uid"}
	root := externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "CompositePodGroup", Namespace: "ns", Name: "root", UID: "root-uid"}
	pods := []string{"ns/pod/pod-uid"}
	for _, tc := range []struct {
		name           string
		info           externaljobinfo.ExternalJobInfo
		groups, legacy []string
	}{
		{"legacy", externaljobinfo.ExternalJobInfo{Pods: pods}, nil, pods},
		{"group", externaljobinfo.ExternalJobInfo{Pods: pods, PodGroup: &leaf}, []string{leaf.Key()}, nil},
		{"composite", externaljobinfo.ExternalJobInfo{Pods: pods, PodGroup: &leaf, CompositePodGroup: &root}, []string{leaf.Key(), root.Key()}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &api.V0044JobInfo{AdminComment: ptr.To(tc.info.ToString())}
			if got := jobGroupKeys(job); !slices.Equal(got, tc.groups) {
				t.Errorf("group keys=%v, want %v", got, tc.groups)
			}
			if got := jobLegacyPodKeys(job); !slices.Equal(got, tc.legacy) {
				t.Errorf("legacy keys=%v, want %v", got, tc.legacy)
			}
		})
	}
}

func TestAllocationIdentity(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: "pod-uid"}}
	for _, kind := range []string{"Pod", "PodGroup", "CompositePodGroup"} {
		t.Run(kind, func(t *testing.T) {
			root := metav1.PartialObjectMetadata{
				TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: kind},
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "group", UID: "group-uid"},
			}
			ir := &slurmjobir.SlurmJobIR{RootPOM: root, Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{pod}}}}}
			got, err := IdentityForAllocation(&pod, ir)
			if err != nil {
				t.Fatal(err)
			}
			want := AllocationIdentity{Index: PodJobIndex, Key: "ns/pod/pod-uid"}
			if kind != "Pod" {
				want = AllocationIdentity{Index: GroupJobIndex, Key: externaljobinfo.NewGroupIdentity(root).Key()}
			}
			if kind == "PodGroup" {
				want.legacyPodKeys = []string{"ns/pod/pod-uid", "ns/pod"}
			}
			if got.Index != want.Index || got.Key != want.Key || !slices.Equal(got.legacyPodKeys, want.legacyPodKeys) {
				t.Fatalf("identity=%+v, want %+v", got, want)
			}
		})
	}
}
