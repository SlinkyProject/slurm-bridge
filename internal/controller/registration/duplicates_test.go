// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/controllercheck"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestDuplicateCleanupWithEmptyPodGroup(t *testing.T) {
	for _, failCancellation := range []bool{false, true} {
		name := "success"
		if failCancellation {
			name = "cancellation failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			w, err := newRegistrationWorld(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			group := &slurmjobir.PodGroup{TypeMeta: w.workloadAPI.PodGroupTypeMeta,
				ObjectMeta: metav1.ObjectMeta{Namespace: "world", Name: "empty", UID: "empty"}}
			key := objectRequest(group)
			if err := w.kube.Create(ctx, group); err != nil {
				t.Fatal(err)
			}
			identity := key.groupIdentity()
			info := externaljobinfo.ExternalJobInfo{PodGroup: &identity, Pods: []string{"world/deleted/deleted-uid"}}
			for _, id := range []int32{100, 102} {
				job := &api.V0044JobInfo{JobId: ptr.To(id), AdminComment: ptr.To(info.ToString()),
					JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}}
				if err := w.live.Create(ctx, job, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.advanceCache(ctx); err != nil {
				t.Fatal(err)
			}
			if failCancellation {
				w.faults.Arm("slurm.cancel", "before-commit")
			}
			result, err := w.reconciler.Reconcile(ctx, key)
			if failCancellation {
				if !errors.Is(err, controllercheck.ErrInjected) {
					t.Fatalf("cleanup error=%v, want cancellation failure", err)
				}
			} else if err != nil {
				t.Fatalf("empty PodGroup should requeue without error: %v", err)
			}
			if errors.Is(err, slurmjobir.ErrorPodGroupNoPods) {
				t.Fatalf("expected membership condition leaked: %v", err)
			}
			if result.RequeueAfter != registrationRetry {
				t.Fatalf("requeue=%v, want %v", result.RequeueAfter, registrationRetry)
			}
			jobs, err := worldJobs(ctx, w.live)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 2 || !worldUnfinished(jobs[0]) || worldUnfinished(jobs[1]) != failCancellation {
				t.Fatalf("unexpected allocations after cleanup: %+v", jobs)
			}
		})
	}
}

func TestGroupAllocations(t *testing.T) {
	job := func(id, leader int32, group, pod string) api.V0044JobInfo {
		info := externaljobinfo.ExternalJobInfo{Pods: []string{pod}}
		if group != "" {
			info.PodGroup = &externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "PodGroup", Namespace: "ns", Name: group, UID: types.UID(group)}
		}
		return api.V0044JobInfo{
			JobId: ptr.To(id), AdminComment: ptr.To(info.ToString()),
			JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
			HetJobId:     &api.V0044Uint32NoValStruct{Set: ptr.To(leader != 0), Number: ptr.To(leader)},
			HetJobOffset: &api.V0044Uint32NoValStruct{Set: ptr.To(leader != 0), Number: ptr.To(id - leader)},
		}
	}
	groupKey := func(name string) string {
		return (&externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: "PodGroup", Namespace: "ns", Name: name, UID: types.UID(name)}).Key()
	}
	finished := job(30, 0, "group", "ns/pod/uid")
	finished.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCOMPLETED}
	for _, tc := range []struct {
		name string
		jobs []api.V0044JobInfo
		want map[string][]int32
	}{
		{"heterogeneous components share a leader", []api.V0044JobInfo{job(10, 10, "group", "ns/pod/uid"), job(17, 10, "group", "ns/other/uid")}, map[string][]int32{groupKey("group"): {10}}},
		{"duplicate allocations", []api.V0044JobInfo{job(10, 0, "group", "ns/pod/uid"), job(20, 0, "group", "ns/pod/uid"), finished}, map[string][]int32{groupKey("group"): {10, 20}}},
		{"legacy UID joins modern group", []api.V0044JobInfo{job(10, 0, "", "ns/pod/uid"), job(20, 0, "group", "ns/pod/uid")}, map[string][]int32{groupKey("group"): {10, 20}}},
		{"legacy name joins modern group", []api.V0044JobInfo{job(10, 0, "", "ns/pod"), job(20, 0, "group", "ns/pod/uid")}, map[string][]int32{groupKey("group"): {10, 20}}},
		{"replaced Pod stays separate", []api.V0044JobInfo{job(10, 0, "", "ns/pod/old"), job(20, 0, "group", "ns/pod/new")}, map[string][]int32{"pod/ns/pod/old": {10}, groupKey("group"): {20}}},
		{"ambiguous legacy ownership stays separate", []api.V0044JobInfo{job(10, 0, "", "ns/pod/uid"), job(20, 0, "first", "ns/pod/uid"), job(30, 0, "second", "ns/pod/uid")}, map[string][]int32{"pod/ns/pod/uid": {10}, groupKey("first"): {20}, groupKey("second"): {30}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(map[string][]int32)
			for key, allocations := range groupAllocations(tc.jobs) {
				for id := range allocations {
					got[key] = append(got[key], id)
				}
				slices.Sort(got[key])
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("allocation groups=%v, want %v", got, tc.want)
			}
		})
	}
}
