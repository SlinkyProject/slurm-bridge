// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"errors"
	"reflect"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"pgregory.net/rapid"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestSelectAllocationProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(2, 8).Draw(rt, "components")
		leader := rapid.Int32Range(100, 100000).Draw(rt, "leader")
		ir := &slurmjobir.SlurmJobIR{RootPOM: metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "CompositePodGroup"}}}
		var jobs []RegisteredJob
		var want []ExternalJob
		for i := range count {
			meta := metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"},
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "leaf-" + strconv.Itoa(i), UID: "uid"}}
			ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{ObjectMeta: meta, Pods: corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: meta.Name, UID: "pod-uid"}}}}})
			// Keep component zero active; finished siblings still belong to it.
			job := RegisteredJob{ExternalJob: ExternalJob{JobId: leader + int32(i), HetJobId: leader, HetJobOffset: int32(i), Finished: i > 0 && rapid.Bool().Draw(rt, "finished")},
				info: externaljobinfo.ExternalJobInfo{PodGroup: externaljobinfo.NewGroupIdentity(meta), Pods: []string{"ns/" + meta.Name + "/pod-uid"}}}
			jobs = append(jobs, job)
			want = append(want, job.ExternalJob)
		}
		for _, input := range [][]RegisteredJob{jobs, rapid.Permutation(jobs).Draw(rt, "shuffle")} {
			got, err := SelectAllocation(ir, input)
			if err != nil || !reflect.DeepEqual(got.Jobs, want) {
				rt.Fatalf("allocation=%+v error=%v, want %v", got, err, want)
			}
		}
		// Replace any non-leading component with history for the identical leaf.
		// It must not complete the active allocation even after arbitrary shuffling.
		gap := rapid.IntRange(1, count-1).Draw(rt, "gap")
		jobs[gap].JobId -= 50
		jobs[gap].HetJobId -= 50
		jobs[gap].Finished = true
		_, err := SelectAllocation(ir, rapid.Permutation(jobs).Draw(rt, "mixed-shuffle"))
		if !errors.Is(err, ErrAllocationMissing) {
			rt.Fatalf("filled component %d from another allocation: error=%v", gap, err)
		}
	})
}
