// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestMatchAllocationWithFinishedComponents(t *testing.T) {
	for _, mode := range []string{"completed sibling", "all finished", "missing component", "historical sibling", "historical duplicates", "active duplicate", "duplicate component"} {
		t.Run(mode, func(t *testing.T) {
			ir := &slurmjobir.SlurmJobIR{RootPOM: metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "CompositePodGroup"}}}
			var jobs []RegisteredJob
			for i, name := range []string{"first", "second"} {
				group := metav1.PartialObjectMetadata{
					TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"},
					ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: "group-uid"},
				}
				ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{ObjectMeta: group})
				jobs = append(jobs, RegisteredJob{
					ExternalJob: ExternalJob{JobId: []int32{100, 107}[i], HetJobId: 100, Finished: i == 0},
					info:        externaljobinfo.ExternalJobInfo{PodGroup: externaljobinfo.NewGroupIdentity(group)},
				})
			}
			complete := true
			var wantErr error
			switch mode {
			case "all finished":
				jobs[1].Finished = true
				complete = false
			case "missing component":
				jobs = jobs[1:]
				complete = false
			case "historical sibling":
				jobs[0].JobId, jobs[0].HetJobId = 50, 50
				complete = false
			case "historical duplicates":
				for _, job := range jobs[:2] {
					job.JobId -= 50
					job.HetJobId, job.Finished = 50, true
					jobs = append(jobs, job)
				}
			case "active duplicate":
				duplicate := jobs[1]
				duplicate.JobId, duplicate.HetJobId = 207, 200
				jobs = append(jobs, duplicate)
				complete, wantErr = false, ErrAmbiguousAllocation
			case "duplicate component":
				duplicate := jobs[0]
				duplicate.JobId = 101
				jobs = append(jobs, duplicate)
				complete, wantErr = false, ErrAmbiguousAllocation
			}
			got, matched, err := MatchAllocationComponents(ir, jobs)
			if matched != complete || !errors.Is(err, wantErr) {
				t.Fatalf("match = %v, %t, %v; want complete=%t error=%v", got, matched, err, complete, wantErr)
			}
			if complete && (len(got) != 2 || got[0].JobId != 100 || !got[0].Finished || got[1].JobId != 107 || got[1].Finished) {
				t.Fatalf("lost active allocation or its finished component: %v", got)
			}
		})
	}
}
