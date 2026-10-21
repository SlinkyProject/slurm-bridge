// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestBuildJobDescAllocationIdentity(t *testing.T) {
	for _, tc := range []struct {
		apiVersion string
		kind       string
	}{
		{"v1", "Pod"},
		{"batch/v1", "Job"},
		{"scheduling.x-k8s.io/v1alpha1", "PodGroup"},
		{"scheduling.k8s.io/v1alpha2", "PodGroup"},
		{"scheduling.k8s.io/v1beta1", "PodGroup"},
		{"scheduling.k8s.io/v1alpha3", "CompositePodGroup"},
	} {
		t.Run(tc.apiVersion+"/"+tc.kind, func(t *testing.T) {
			pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: "pod-uid"}}
			root := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: tc.apiVersion, Kind: tc.kind}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "root", UID: "root-uid"}}
			leaf := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "leaf", UID: "leaf-uid"}}
			ir := &slurmjobir.SlurmJobIR{RootPOM: root, Components: []slurmjobir.SlurmJobComponent{{ObjectMeta: leaf, Pods: corev1.PodList{Items: []corev1.Pod{pod}}}}}
			want := externaljobinfo.ExternalJobInfo{Pods: []string{"ns/pod/pod-uid"}}
			switch tc.kind {
			case "PodGroup":
				want.PodGroup = &externaljobinfo.GroupIdentity{APIGroup: root.GroupVersionKind().Group, Kind: "PodGroup", Namespace: "ns", Name: "root", UID: "root-uid"}
			case "CompositePodGroup":
				want.CompositePodGroup = &externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "CompositePodGroup", Namespace: "ns", Name: "root", UID: "root-uid"}
				want.PodGroup = &externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "PodGroup", Namespace: "ns", Name: "leaf", UID: "leaf-uid"}
			}
			for _, update := range []bool{false, true} {
				desc, err := (&realSlurmControl{}).buildJobDesc(root, ir.Components[0], update)
				if err != nil {
					t.Fatal(err)
				}
				var got externaljobinfo.ExternalJobInfo
				if err := externaljobinfo.ParseIntoExternalJobInfo(desc.AdminComment, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("update=%t: metadata=%s, want %s", update, got.ToString(), want.ToString())
				}
			}
		})
	}
}
