// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestIndexedAllocationMembership(t *testing.T) {
	for _, state := range []api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING, api.V0044JobInfoJobStateRUNNING} {
		for _, tc := range []struct {
			name, uid, recorded string
			want                bool
		}{
			{"original", "old", "ns/pod/old", true},
			{"replacement", "new", "ns/pod/old", false},
			{"legacy identity", "new", "ns/pod", true},
		} {
			t.Run(string(state)+"/"+tc.name, func(t *testing.T) {
				root := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "gang", UID: "gang-uid"}}
				info := externaljobinfo.ExternalJobInfo{PodGroup: externaljobinfo.NewGroupIdentity(root), Pods: []string{tc.recorded}}
				raw := &api.V0044JobInfo{JobId: ptr.To(int32(10)), AdminComment: ptr.To(info.ToString()), JobState: ptr.To([]api.V0044JobInfoJobState{state})}
				slurm := fake.NewFakeClient(raw)
				index, err := NewPodIndex(nil, slurm.GetInformer(api.ObjectTypeV0044JobInfo))
				if err != nil {
					t.Fatal(err)
				}
				pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: types.UID(tc.uid)}}
				ir := &slurmjobir.SlurmJobIR{RootPOM: root, Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{pod}}}}}
				got, err := index.GetAllocation(t.Context(), &pod, ir)
				if tc.want {
					if err != nil || len(got.Jobs) != 1 || got.Jobs[0].JobId != 10 {
						t.Fatalf("jobs=%v err=%v", got, err)
					}
				} else if !errors.Is(err, ErrAllocationMembership) {
					t.Fatalf("error=%v, want membership rejection", err)
				}
			})
		}
	}
}

func TestCompositeAllocationUsesRecordedComponents(t *testing.T) {
	root := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha3", Kind: "CompositePodGroup"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "root", UID: "root-uid"}}
	ir := &slurmjobir.SlurmJobIR{RootPOM: root}
	var records []*api.V0044JobInfo
	for i, name := range []string{"first", "second", "late"} {
		group := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name)}}
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name + "-pod")}}
		ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{ObjectMeta: group, Pods: corev1.PodList{Items: []corev1.Pod{pod}}})
		if i == 2 {
			continue
		}
		info := externaljobinfo.ExternalJobInfo{CompositePodGroup: externaljobinfo.NewGroupIdentity(root), PodGroup: externaljobinfo.NewGroupIdentity(group), Pods: []string{externaljobinfo.PodKey(&pod)}}
		records = append(records, &api.V0044JobInfo{JobId: ptr.To(int32(100 + 7*i)), AdminComment: ptr.To(info.ToString()),
			JobState:     ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}),
			HetJobId:     &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(100))},
			HetJobOffset: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(i))}, HetJobIdSet: ptr.To("100,107")})
	}
	slurm := fake.NewFakeClient(records[0])
	index, err := NewPodIndex(nil, slurm.GetInformer(api.ObjectTypeV0044JobInfo))
	if err != nil {
		t.Fatal(err)
	}
	first := &ir.Components[0].Pods.Items[0]
	if _, err := index.GetAllocation(t.Context(), first, ir); !errors.Is(err, ErrAllocationMissing) {
		t.Fatalf("incomplete informer snapshot accepted: %v", err)
	}
	if err := slurm.Create(t.Context(), records[1], nil); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		allocation, err := index.GetAllocation(t.Context(), &ir.Components[i].Pods.Items[0], ir)
		if err != nil || len(allocation.Jobs) != 2 || len(allocation.IR.Components) != 2 {
			t.Fatalf("recorded component blocked by late child: %+v, %v", allocation, err)
		}
		if allocation.Jobs[i].JobId != int32(100+7*i) || allocation.IR.Components[i].ObjectMeta.UID != ir.Components[i].ObjectMeta.UID {
			t.Fatalf("component identities crossed: %+v", allocation)
		}
	}
	if len(ir.Components) != 3 {
		t.Fatal("lookup mutated shared workload")
	}
	if _, err := index.GetAllocation(t.Context(), &ir.Components[2].Pods.Items[0], ir); !errors.Is(err, ErrAllocationMembership) {
		t.Fatalf("late child adopted allocation: %v", err)
	}
}

func TestEmptiedLeafDoesNotBlockOriginalSibling(t *testing.T) {
	root := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha3", Kind: "CompositePodGroup"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "root", UID: "root"}}
	ir := &slurmjobir.SlurmJobIR{RootPOM: root}
	var jobs []RegisteredJob
	for i, name := range []string{"first", "second"} {
		group := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name)}}
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name)}}
		info := externaljobinfo.ExternalJobInfo{CompositePodGroup: externaljobinfo.NewGroupIdentity(root), PodGroup: externaljobinfo.NewGroupIdentity(group), Pods: []string{externaljobinfo.PodKey(&pod)}}
		job, err := RegisteredJobFromInfo(&api.V0044JobInfo{JobId: ptr.To(int32(100 + i)), AdminComment: ptr.To(info.ToString()), JobState: ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING}), HetJobId: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(100))}, HetJobOffset: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(i))}, HetJobIdSet: ptr.To("100-101")})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			job.Finished = true
			job.Pending = false
		}
		jobs = append(jobs, job)
		// The original second Pod has completed; only a newly arrived Pod remains.
		if i == 1 {
			pod.UID = "late"
			pod.Name = "late"
		}
		ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{ObjectMeta: group, Pods: corev1.PodList{Items: []corev1.Pod{pod}}})
	}
	got, err := SelectAllocation(ir, jobs)
	if err != nil {
		t.Fatalf("surviving first member blocked by late member of second leaf: %v", err)
	}
	if got.IR.ComponentOf("ns", "first") < 0 {
		t.Fatal("lost original sibling")
	}
}
