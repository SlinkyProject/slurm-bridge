// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// These properties run with the normal Go test suite. For a longer campaign:
// go test ./internal/scheduler/plugins/slurmbridge -run 'TestScheduler.*Properties' -rapid.checks=1000
// Rapid reports a replay seed and shrinks failing generated cases.

// Exercise retries while the scheduler snapshot advances ahead of the informer.
// The oracle checks ownership and stability without prescribing which free node
// the first scheduling attempt chooses.
func TestSchedulerPlacementRetryProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(1, 16).Draw(rt, "members")
		pods, nodes, allocation := schedulingPropertyGang(count, count+rapid.IntRange(0, 4).Draw(rt, "spare-nodes"))
		original := (&corev1.PodList{Items: pods}).DeepCopy()
		beforeNodes := allocation.Clone()
		initial, err := nativeAssignments(pods, allocation, internalcache.NewSnapshot(nil, nodes).NodeInfos())
		if err != nil {
			rt.Fatal(err)
		}
		var assumed []*corev1.Pod
		for _, index := range rapid.Permutation(schedulingPropertyIndices(count)).Draw(rt, "binding-order") {
			// Either the framework has assumed the Pod, or its binding has also
			// reached the informer. Both observations must preserve the decision.
			placed := pods[index].DeepCopy()
			placed.Spec.NodeName = initial[client.ObjectKeyFromObject(placed)]
			assumed = append(assumed, placed)
			if rapid.Bool().Draw(rt, "binding-observed") {
				pods[index].Spec.NodeName = placed.Spec.NodeName
			}
			shuffled := rapid.Permutation(pods).Draw(rt, "member-order")
			snapshot := internalcache.NewSnapshot(assumed, rapid.Permutation(nodes).Draw(rt, "node-order"))
			got, err := nativeAssignments(shuffled, allocation, snapshot.NodeInfos())
			if err != nil {
				rt.Fatal(err)
			}
			if !reflect.DeepEqual(got, initial) {
				rt.Fatalf("retry reshuffled placement: got %v, initial %v", got, initial)
			}
			used := sets.New[string]()
			sb := &SlurmBridge{}
			for i := range shuffled {
				pod := &shuffled[i]
				assigned := got[client.ObjectKeyFromObject(pod)]
				if !allocation.Has(assigned) || used.Has(assigned) {
					rt.Fatalf("invalid or duplicate assignment %q: %v", assigned, got)
				}
				used.Insert(assigned)
				state := framework.NewCycleState()
				state.Write(stateKey, &stateData{native: &nativeAllocation{}, nodeName: assigned})
				// Legacy annotations must never override a native scheduling decision.
				for _, candidate := range nodes {
					info := framework.NewNodeInfo()
					info.SetNode(candidate)
					status := sb.Filter(rt.Context(), state, pod, info)
					if status.IsSuccess() != (candidate.Name == assigned) {
						rt.Fatalf("Filter accepted wrong placement: assigned=%q candidate=%q status=%v", assigned, candidate.Name, status)
					}
				}
			}
		}
		// Ignore intentional binding updates when checking that placement did not
		// mutate the input Pods or consume the caller's node set.
		for i := range pods {
			pods[i].Spec.NodeName = ""
		}
		if !reflect.DeepEqual(pods, original.Items) || !allocation.Equal(beforeNodes) {
			rt.Fatal("placement mutated input")
		}
	})
}

func TestSchedulerRejectsInvalidPlacementProperties(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"too-few-nodes", "duplicate-binding", "outside-allocation", "conflicting-snapshot"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			rapid.Check(t, func(rt *rapid.T) {
				count := rapid.IntRange(2, 16).Draw(rt, "members")
				pods, nodes, allocation := schedulingPropertyGang(count, count)
				order := rapid.Permutation(schedulingPropertyIndices(count)).Draw(rt, "members-to-change")
				var assumed []*corev1.Pod
				switch fault {
				case "too-few-nodes":
					allocation.Delete(nodes[rapid.IntRange(0, count-1).Draw(rt, "missing-node")].Name)
				case "duplicate-binding":
					node := nodes[rapid.IntRange(0, count-1).Draw(rt, "duplicate-node")].Name
					pods[order[0]].Spec.NodeName, pods[order[1]].Spec.NodeName = node, node
				case "outside-allocation":
					pods[order[0]].Spec.NodeName = "outside"
				case "conflicting-snapshot":
					pods[order[0]].Spec.NodeName = nodes[0].Name
					previous := pods[order[0]].DeepCopy()
					previous.Spec.NodeName = nodes[1].Name
					assumed = append(assumed, previous)
				}
				got, err := nativeAssignments(rapid.Permutation(pods).Draw(rt, "member-order"), allocation, internalcache.NewSnapshot(assumed, nodes).NodeInfos())
				if err == nil || got != nil {
					rt.Fatalf("accepted %s: assignments=%v error=%v", fault, got, err)
				}
			})
		})
	}
}

// A scheduling cycle may observe changing Slurm state, but must never adopt
// another allocation. After a rejected observation the original remains usable.
func TestSchedulerAllocationPinningProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(1, 8).Draw(rt, "components")
		leader := rapid.Int32Range(1, 1000000).Draw(rt, "leader")
		ir := &slurmjobir.SlurmJobIR{}
		jobs := make([]slurmcontrol.ExternalJob, count)
		for i := range count {
			pods, _, _ := schedulingPropertyGang(rapid.IntRange(1, 4).Draw(rt, "members"), 0)
			for p := range pods {
				pods[p].Namespace = fmt.Sprintf("component-%d", i)
				pods[p].Labels = map[string]string{wellknown.LabelExternalJobId: strconv.Itoa(int(leader) + i)}
			}
			ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{Pods: corev1.PodList{Items: pods}})
			jobs[i] = slurmcontrol.ExternalJob{JobId: leader + int32(i), HetJobId: leader}
		}
		s := &stateData{slurmJobIR: ir}
		pod := &ir.Components[rapid.IntRange(0, count-1).Draw(rt, "scheduling-component")].Pods.Items[0]
		if status := s.checkAllocation(pod, jobs, nil); !status.IsSuccess() {
			rt.Fatal(status)
		}
		for range rapid.IntRange(1, 20).Draw(rt, "observations") {
			observed := slices.Clone(jobs)
			changed := rapid.IntRange(0, count-1).Draw(rt, "changed-component")
			switch rapid.SampledFrom([]string{"job", "leader", "missing"}).Draw(rt, "change") {
			case "job":
				observed[changed].JobId += 1000001
			case "leader":
				observed[changed].HetJobId += 1000001
			case "missing":
				observed = append(observed[:changed], observed[changed+1:]...)
			}
			if status := s.checkAllocation(pod, observed, nil); status.IsSuccess() {
				rt.Fatalf("accepted replacement allocation: %v", observed)
			}
			if !reflect.DeepEqual(s.allocation, jobs) {
				rt.Fatal("rejected observation changed pinned allocation")
			}
			if status := s.checkAllocation(pod, slices.Clone(jobs), nil); !status.IsSuccess() {
				rt.Fatalf("original allocation no longer usable: %v", status)
			}
		}
	})
}

func schedulingPropertyGang(members, nodeCount int) ([]corev1.Pod, []*corev1.Node, sets.Set[string]) {
	pods := make([]corev1.Pod, members)
	for i := range pods {
		pods[i] = corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: fmt.Sprintf("ns-%d", i%2), Name: fmt.Sprintf("pod-%d", i), UID: types.UID(fmt.Sprintf("uid-%d", i)), Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "stale-node"}}}
	}
	nodes := make([]*corev1.Node, nodeCount)
	allocation := sets.New[string]()
	for i := range nodes {
		name := fmt.Sprintf("node-%d", i)
		nodes[i] = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
		allocation.Insert(name)
	}
	return pods, nodes, allocation
}

func schedulingPropertyIndices(count int) []int {
	indices := make([]int, count)
	for i := range indices {
		indices[i] = i
	}
	return indices
}

// Inject a failed eligibility write at any component, then retry the same cycle.
// No release is permitted until all updates have succeeded, and temporary
// priority suppression must not change the workload's requested priorities.
func TestSchedulerHoldReleaseProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(1, 12).Draw(rt, "components")
		ir := &slurmjobir.SlurmJobIR{}
		priorities := make([]*int32, count)
		for i := range count {
			if rapid.Bool().Draw(rt, "explicit-priority") {
				priorities[i] = ptr.To(rapid.Int32Range(0, 100000).Draw(rt, "priority"))
			}
			ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{
				Pods:    corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("pod-%d", i), Labels: map[string]string{wellknown.LabelExternalJobId: strconv.Itoa(100 + i)}}}}},
				JobInfo: slurmjobir.SlurmJobIRJobInfo{Priority: priorities[i]},
			})
		}
		control := &schedulingPropertyReleaseControl{rt: rt, failAt: rapid.IntRange(0, count-1).Draw(rt, "failed-update"), priorities: priorities}
		sb := &SlurmBridge{slurmControl: control}
		state := &stateData{slurmJobIR: ir}
		if status := sb.updateExternalJob(rt.Context(), state, true); status.IsSuccess() {
			rt.Fatal("eligibility failure ignored")
		}
		if len(control.released) != 0 {
			rt.Fatal("released a partially updated gang")
		}
		for i := range ir.Components {
			if !reflect.DeepEqual(ir.Components[i].JobInfo.Priority, priorities[i]) {
				rt.Fatal("failed update lost requested priority")
			}
		}
		control.failAt = -1
		control.updated = sets.New[int]()
		if status := sb.updateExternalJob(rt.Context(), state, true); !status.IsSuccess() {
			rt.Fatalf("retry failed: %v", status)
		}
		if len(control.released) != count {
			rt.Fatalf("released %d components, want %d", len(control.released), count)
		}
		for i := range ir.Components {
			if !reflect.DeepEqual(ir.Components[i].JobInfo.Priority, priorities[i]) {
				rt.Fatal("retry lost requested priority")
			}
		}
	})
}

type schedulingPropertyReleaseControl struct {
	releaseControl
	rt         *rapid.T
	failAt     int
	priorities []*int32
	updated    sets.Set[int]
	released   sets.Set[int32]
}

func (c *schedulingPropertyReleaseControl) UpdateJob(_ context.Context, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) (int32, error) {
	index := ir.ComponentOf(pod.Namespace, pod.Name)
	if index < 0 || index > 12 {
		return 0, fmt.Errorf("component index outside generated range: %d", index)
	}
	if ir.Components[index].JobInfo.Priority != nil {
		c.rt.Fatal("eligibility update could release a held component via priority")
	}
	if index == c.failAt {
		return 0, errors.New("injected eligibility write failure")
	}
	if c.updated == nil {
		c.updated = sets.New[int]()
	}
	c.updated.Insert(index)
	return int32(100 + index), nil
}

func (c *schedulingPropertyReleaseControl) ReleaseJob(_ context.Context, id int32, priority *int32) error {
	if len(c.updated) != len(c.priorities) {
		c.rt.Fatal("hold released before every eligibility update succeeded")
	}
	index := int(id) - 100
	if index < 0 || index >= len(c.priorities) {
		c.rt.Fatalf("released unknown job %d", id)
	}
	if !reflect.DeepEqual(priority, c.priorities[index]) {
		c.rt.Fatalf("job %d released with wrong priority", id)
	}
	if c.released == nil {
		c.released = sets.New[int32]()
	}
	if c.released.Has(id) {
		c.rt.Fatalf("released job %d twice", id)
	}
	c.released.Insert(id)
	return nil
}
