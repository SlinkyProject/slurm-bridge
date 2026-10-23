// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"cmp"
	"reflect"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"

	api "github.com/SlinkyProject/slurm-client/api/v0044"

	schedulercontrol "github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestSurvivingAllocationProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		type preference struct {
			id   int32
			rank int
		}
		var order []preference
		active := map[int32][]api.V0044JobInfo{}
		id := rapid.Int32Range(1, 1000).Draw(rt, "first-id")
		states := []api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING, api.V0044JobInfoJobStatePENDING,
			api.V0044JobInfoJobStateRUNNING, api.V0044JobInfoJobStateSUSPENDED, api.V0044JobInfoJobStateCOMPLETING}
		for range rapid.IntRange(1, 10).Draw(rt, "allocations") {
			id += rapid.Int32Range(10, 100).Draw(rt, "id-gap")
			best := 0
			for offset := range rapid.IntRange(1, 4).Draw(rt, "components") {
				class := rapid.IntRange(0, len(states)-1).Draw(rt, "state")
				best = max(best, min(class, 2))
				priority := int32(0)
				if class != 0 {
					priority = rapid.Int32Range(1, 1000).Draw(rt, "priority")
				}
				active[id] = append(active[id], api.V0044JobInfo{
					JobId: ptr.To(id + int32(offset)), JobState: &[]api.V0044JobInfoJobState{states[class]},
					Priority:     &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(priority)},
					HetJobId:     &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(id)},
					HetJobOffset: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(offset))},
				})
			}
			order = append(order, preference{id, best})
		}
		slices.SortFunc(order, func(a, b preference) int {
			if a.rank != b.rank {
				return cmp.Compare(b.rank, a.rank)
			}
			return cmp.Compare(a.id, b.id)
		})
		permuted := map[int32][]api.V0044JobInfo{}
		for _, p := range rapid.Permutation(order).Draw(rt, "allocation-order") {
			permuted[p.id] = rapid.Permutation(active[p.id]).Draw(rt, "component-order")
		}
		for _, input := range []map[int32][]api.V0044JobInfo{active, permuted} {
			got, err := survivingAllocation(input)
			if err != nil || got != order[0].id {
				rt.Fatalf("survivor=%d error=%v, preference=%v", got, err, order)
			}
		}
	})
}

func TestSubmissionAllowedProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		r := &Reconciler{schedulerName: "slurm-bridge"}
		members := make([]corev1.Pod, rapid.IntRange(0, 8).Draw(rt, "members"))
		allEligible := true
		for i := range members {
			members[i].Spec.SchedulerName = r.schedulerName
			kind := rapid.IntRange(0, 4).Draw(rt, "eligibility")
			allEligible = allEligible && kind == 0
			switch kind {
			case 1:
				members[i].Spec.NodeName = "bound"
			case 2:
				members[i].DeletionTimestamp = &metav1.Time{}
			case 3:
				members[i].Status.Phase = corev1.PodSucceeded
			case 4:
				members[i].Spec.SchedulerName = "another-scheduler"
			}
		}
		id := rapid.Int32Range(1, 100000).Draw(rt, "allocation")
		jobs := []slurmjob.RegisteredJob{{ExternalJob: slurmjob.ExternalJob{JobId: id + 1, HetJobId: id, Finished: true}}}
		var cooldown *int32
		mode := rapid.IntRange(0, 3).Draw(rt, "cooldown")
		switch mode {
		case 1:
			cooldown = ptr.To(int32(0))
		case 2:
			cooldown = ptr.To(id)
		case 3:
			cooldown = ptr.To(id + 1)
		}
		got := submissionAllowed(jobs, cooldown, members, r.eligible)
		if want := allEligible && (mode == 0 || mode == 2); got != want {
			rt.Fatalf("allowed=%t want=%t eligible=%t cooldown=%v", got, want, allEligible, cooldown)
		}
	})
}

func TestExternalJobInfoProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		root := metav1.PartialObjectMetadata{TypeMeta: rapid.SampledFrom([]metav1.TypeMeta{
			{APIVersion: "v1", Kind: "Pod"}, {APIVersion: "batch/v1", Kind: "Job"},
			{APIVersion: "scheduling.x-k8s.io/v1alpha1", Kind: "PodGroup"},
			{APIVersion: "scheduling.k8s.io/v1alpha2", Kind: "PodGroup"},
			{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"},
			{APIVersion: "scheduling.k8s.io/v1alpha3", Kind: "CompositePodGroup"},
		}).Draw(rt, "root-kind"), ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "root", UID: types.UID(rapid.StringMatching(`[a-z0-9]{1,12}`).Draw(rt, "root-uid"))}}
		component := slurmjobir.SlurmJobComponent{ObjectMeta: metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"},
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "leaf", UID: "leaf-uid"}}}
		for i := range rapid.IntRange(1, 8).Draw(rt, "members") {
			component.Pods.Items = append(component.Pods.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: root.Namespace, Name: "pod-" + strconv.Itoa(i), UID: types.UID(rapid.StringMatching(`[a-z0-9]{1,12}`).Draw(rt, "pod-uid"))}})
		}
		info := schedulercontrol.ExternalJobInfoFor(root, component)
		var decoded externaljobinfo.ExternalJobInfo
		if err := externaljobinfo.ParseIntoExternalJobInfo(ptr.To(info.ToString()), &decoded); err != nil {
			rt.Fatal(err)
		}
		if !reflect.DeepEqual(info, decoded) || len(decoded.Pods) != len(component.Pods.Items) {
			rt.Fatalf("identity round trip: %v -> %v", info, decoded)
		}
		var queued []Request
		enqueueJob(&api.V0044JobInfo{AdminComment: ptr.To(info.ToString())}, func(key Request) { queued = append(queued, key) })
		group := decoded.PodGroup
		if root.Kind == "CompositePodGroup" {
			group = decoded.CompositePodGroup
			if !ptr.Equal(decoded.PodGroup, externaljobinfo.NewGroupIdentity(component.ObjectMeta)) {
				rt.Fatal("lost component identity")
			}
		}
		for _, pod := range component.Pods.Items {
			key := allocationKey(&pod, root)
			if group != nil && group.Key() != key.groupIdentity().Key() {
				rt.Fatalf("writer group %s differs from allocation %s", group.Key(), key)
			}
			if !slices.Contains(decoded.Pods, externaljobinfo.PodKey(&pod)) || !slices.Contains(queued, key) {
				rt.Fatalf("Pod %s identity lost in %v / %v", pod.Name, decoded, queued)
			}
		}
	})
}
