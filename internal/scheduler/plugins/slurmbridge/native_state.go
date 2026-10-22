// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/sets"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-client/pkg/hostlist"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// nativeAllocation belongs to one framework group scheduling attempt. Translation,
// membership and allocation observation are shared by all components, including
// siblings reached through a CompositePodGroup. A new attempt always rebuilds the
// snapshot: no membership, allocation or feasibility result survives a retry.
// Members retain this pointer after the framework detaches the group cycle for
// binding. Once guards publish binding validation and resources; snapshots are read-only.
type nativeAllocation struct {
	ir               *slurmjobir.SlurmJobIR
	jobs             []slurmcontrol.ExternalJob
	status           *fwk.Status
	components       []nativeComponent
	members          map[client.ObjectKey]nativeMember
	validationOnce   sync.Once
	validationStatus *fwk.Status
}

type nativeMember struct {
	pod       *corev1.Pod
	component int
}

type nativeComponent struct {
	prepared      bool
	status        *fwk.Status
	assignments   map[client.ObjectKey]string
	slurmNodes    []string
	resourceOnce  sync.Once
	resources     map[string]*slurmcontrol.NodeResources
	bindingStatus *fwk.Status
}

// Use the outermost scheduling attempt, while translation keeps independent
// allocations beneath basic composite boundaries separate in the member index.
func nativeSchedulingCycle(state fwk.CycleState, pod *corev1.Pod) *nativeGroupCycle {
	if pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
		return nil
	}
	groupState := state.GetPodGroupSchedulingCycle()
	if groupState == nil {
		return nil
	}
	for {
		parent := groupState.GetParentPlacementCycleState()
		if parent == nil || parent.GetPodGroupSchedulingCycle() == nil {
			break
		}
		groupState = parent.GetPodGroupSchedulingCycle()
	}
	if data, err := groupState.Read(nativeGroupCycleKey); err == nil {
		return data.(*nativeGroupCycle)
	}
	group := &nativeGroupCycle{}
	groupState.Write(nativeGroupCycleKey, group)
	return group
}

// Only eligibility fields are mutated after translation. Give PostFilter its own
// component/JobInfo values; Pods, metadata and pointed-to scalar values stay read-only.
func copySchedulingIR(ir *slurmjobir.SlurmJobIR) *slurmjobir.SlurmJobIR {
	copy := *ir
	copy.Components = slices.Clone(ir.Components)
	return &copy
}

// assignedNode uses the current native decision, including an empty decision for
// pending allocations. Only the legacy path may fall back to Pod metadata.
func (s *stateData) assignedNode(pod *corev1.Pod) string {
	if s.native != nil || s.nodeName != "" {
		return s.nodeName
	}
	return pod.Annotations[wellknown.AnnotationExternalJobNode]
}

func (sb *SlurmBridge) preFilterNative(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, group *nativeGroupCycle) (*fwk.PreFilterResult, *fwk.Status) {
	s := &stateData{}
	state.Write(stateKey, s)
	key := client.ObjectKeyFromObject(pod)
	allocation := group.allocations[key]
	if allocation == nil {
		ir, err := slurmjobir.TranslateToSlurmJobIR(sb.Client, sb.registry(), sb.workloadAPI, ctx, pod)
		if err != nil {
			if errors.Is(err, slurmjobir.ErrorPodGroupUnsupported) {
				return nil, fwk.NewStatus(fwk.UnschedulableAndUnresolvable, err.Error())
			}
			return nil, fwk.AsStatus(err)
		}
		// A newly arriving/replaced member must not create a second decision for
		// an allocation already prepared in this attempt. Retry with a new cycle.
		for _, component := range ir.Components {
			for i := range component.Pods.Items {
				if previous := group.allocations[client.ObjectKeyFromObject(&component.Pods.Items[i])]; previous != nil {
					s.native = previous
					return nil, fwk.NewStatus(fwk.Unschedulable, "gang membership changed during scheduling")
				}
			}
		}
		allocation = &nativeAllocation{ir: ir, components: make([]nativeComponent, len(ir.Components)), members: make(map[client.ObjectKey]nativeMember)}
		if group.allocations == nil {
			group.allocations = make(map[client.ObjectKey]*nativeAllocation)
		}
		for index := range ir.Components {
			component := &ir.Components[index]
			for i := range component.Pods.Items {
				member := &component.Pods.Items[i]
				key := client.ObjectKeyFromObject(member)
				group.allocations[key] = allocation
				allocation.members[key] = nativeMember{pod: member, component: index}
			}
		}
		allocation.status = sb.prepareNativeAllocation(ctx, pod, allocation)
	}
	s.native = allocation
	if !allocation.status.IsSuccess() {
		return nil, allocation.status
	}
	member, found := allocation.members[key]
	if !found {
		return nil, fwk.NewStatus(fwk.Unschedulable, "Pod is absent from gang snapshot")
	}
	index := member.component
	if !sameNativeMember(member.pod, pod) || !equality.Semantic.DeepEqual(member.pod.Spec, pod.Spec) {
		return nil, fwk.NewStatus(fwk.Unschedulable, "Pod identity or requirements changed during gang scheduling")
	}

	s.slurmJobIR, s.allocation, s.native = allocation.ir, allocation.jobs, allocation
	job := allocation.jobs[index]
	if job.Finished {
		s.slurmJobIR = nil
		return nil, fwk.NewStatus(fwk.Unschedulable, "Slurm component has finished")
	}
	if job.Pending || job.Nodes == "" {
		return nil, nil
	}
	placement := &allocation.components[index]
	if !placement.prepared {
		placement.prepared = true
		placement.status = sb.prepareNativePlacement(ctx, pod, s, index)
	}
	if !placement.status.IsSuccess() {
		return nil, placement.status
	}
	s.nodeName = placement.assignments[key]
	if s.nodeName == "" {
		return nil, fwk.NewStatus(fwk.Unschedulable, "gang member has no node assignment")
	}
	return &fwk.PreFilterResult{NodeNames: sets.New(s.nodeName)}, nil
}

func (sb *SlurmBridge) prepareNativeAllocation(ctx context.Context, pod *corev1.Pod, allocation *nativeAllocation) *fwk.Status {
	for _, component := range allocation.ir.Components {
		for _, member := range component.Pods.Items {
			if member.Spec.ResourceClaims != nil {
				return fwk.NewStatus(fwk.Unschedulable, ErrorPodWithResourceClaim.Error())
			}
			if hasRequiredAffinity(&member) {
				return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodWithRequiredAffinity.Error())
			}
		}
		if err := sb.validateDeviceClassRequestsForPods(ctx, component.Pods.Items, ptr.Deref(component.JobInfo.Exclusive, true)); err != nil {
			return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, err.Error())
		}
	}
	jobs, err := sb.slurmControl.GetAllocation(ctx, pod, allocation.ir)
	s := &stateData{slurmJobIR: allocation.ir}
	if status := s.checkAllocation(pod, jobs, err); !status.IsSuccess() {
		return status
	}
	allocation.jobs = slices.Clone(jobs)
	status := slurmjobir.PreFilter(sb.Client, sb.registry(), sb.handle, sb.workloadAPI, ctx, pod, allocation.ir)
	if !status.IsSuccess() && slices.Contains(status.Reasons(), slurmjobir.ErrorExternalJobInvalid.Error()) {
		if err := sb.deleteExternalJob(ctx, pod); err != nil {
			return fwk.AsStatus(err)
		}
	}
	return status
}

func (sb *SlurmBridge) prepareNativePlacement(ctx context.Context, pod *corev1.Pod, s *stateData, index int) *fwk.Status {
	allocation := s.native
	job := allocation.jobs[index]
	slurmNodes, err := hostlist.Expand(job.Nodes)
	if err != nil {
		return fwk.AsStatus(err)
	}
	nodes, err := sb.slurmToKubeNodes(ctx, slurmNodes)
	if err != nil {
		if errors.Is(err, ErrorNoKubeNodeMatch) {
			if status := sb.confirmPlacement(ctx, pod, job); !status.IsSuccess() {
				return status
			}
		}
		return sb.handleAllocatedNodeError(ctx, s, pod, err)
	}
	component := &allocation.ir.Components[index]
	assignments, err := nativeAssignments(component.Pods.Items, nodes, sb.handle.SnapshotSharedLister().NodeInfos())
	if err != nil {
		return fwk.NewStatus(fwk.Unschedulable, err.Error())
	}
	// Placement belongs to this scheduling attempt. The framework carries it
	// through binding and owns native PodGroup status; publishing annotations
	// here would add one serial API write per member and race Pod updates.
	allocation.components[index].assignments = assignments
	allocation.components[index].slurmNodes = slurmNodes
	return nil
}

func nativeAssignments(pods []corev1.Pod, nodes sets.Set[string], snapshot fwk.NodeInfoLister) (map[client.ObjectKey]string, error) {
	// The framework snapshot also includes assumed Pods whose binding has not
	// reached the informer yet. Reserve those placements by UID across retries.
	placed := make(map[client.ObjectKey]*corev1.Pod)
	for node := range nodes {
		info, err := snapshot.Get(node)
		if err != nil {
			return nil, err
		}
		for _, occupant := range info.GetPods() {
			pod := occupant.GetPod()
			placed[client.ObjectKeyFromObject(pod)] = pod
		}
	}
	available := nodes.Clone()
	assignments := make(map[client.ObjectKey]string, len(pods))
	for i := range pods {
		pod := &pods[i]
		node := pod.Spec.NodeName
		if previous := placed[client.ObjectKeyFromObject(pod)]; previous != nil && previous.UID == pod.UID {
			if node != "" && node != previous.Spec.NodeName {
				return nil, fmt.Errorf("gang member %s has conflicting bound nodes", client.ObjectKeyFromObject(pod))
			}
			node = previous.Spec.NodeName
		}
		if node == "" {
			continue
		}
		if !available.Has(node) {
			return nil, fmt.Errorf("gang node %q is outside the allocation or assigned more than once", node)
		}
		available.Delete(node)
		assignments[client.ObjectKeyFromObject(pod)] = node
	}
	remaining := sets.List(available)
	ordered := slices.Clone(pods)
	slices.SortFunc(ordered, func(a, b corev1.Pod) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	for i := range ordered {
		key := client.ObjectKeyFromObject(&ordered[i])
		if assignments[key] != "" {
			continue
		}
		if len(remaining) == 0 {
			return nil, ErrorNoKubeNode
		}
		assignments[key], remaining = remaining[0], remaining[1:]
	}
	return assignments, nil
}

// Binding validates the allocation once per attempt and each live component once.
// Later members deliberately reuse that observation: neither this snapshot nor a
// per-Pod GET makes Slurm cancellation atomic with Kubernetes binding. Retries
// rebuild all state, and each member must still match its pinned identity/node.
func (a *nativeAllocation) resourcesFor(ctx context.Context, control slurmcontrol.SlurmControlInterface, pod *corev1.Pod, node string) (*slurmcontrol.NodeResources, *fwk.Status) {
	member, found := a.members[client.ObjectKeyFromObject(pod)]
	if !found || !sameNativeMember(member.pod, pod) {
		return nil, fwk.NewStatus(fwk.Unschedulable, "Pod identity changed before gang binding")
	}
	a.validationOnce.Do(func() {
		jobs, err := control.GetAllocation(ctx, pod, a.ir)
		state := &stateData{slurmJobIR: a.ir, allocation: a.jobs}
		a.validationStatus = state.checkAllocation(pod, jobs, err)
	})
	if !a.validationStatus.IsSuccess() {
		return nil, a.validationStatus
	}
	component := &a.components[member.component]
	component.resourceOnce.Do(func() {
		live, err := control.GetJob(ctx, pod)
		if err != nil {
			component.bindingStatus = fwk.AsStatus(err)
			return
		}
		expected := a.jobs[member.component]
		nodes, err := hostlist.Expand(live.Nodes)
		if err != nil {
			component.bindingStatus = fwk.AsStatus(err)
			return
		}
		if live.JobId != expected.JobId || live.HetJobId != expected.HetJobId ||
			live.Pending || live.Finished || !sets.New(nodes...).HasAll(component.slurmNodes...) {
			component.bindingStatus = fwk.NewStatus(fwk.Unschedulable, "Slurm allocation no longer owns the gang's nodes")
			return
		}
		component.resources, err = control.GetResourcesForNodes(ctx, pod, component.slurmNodes)
		component.bindingStatus = fwk.AsStatus(err)
	})
	if !component.bindingStatus.IsSuccess() {
		return nil, component.bindingStatus
	}
	resources, found := component.resources[node]
	if !found {
		return nil, fwk.AsStatus(fmt.Errorf("Slurm resource snapshot has no allocation for node %q", node))
	}
	return resources, nil
}

func sameNativeMember(expected, pod *corev1.Pod) bool {
	return expected.UID == pod.UID &&
		expected.Labels[wellknown.LabelExternalJobId] == pod.Labels[wellknown.LabelExternalJobId] &&
		expected.Labels[wellknown.LabelExternalHetJobId] == pod.Labels[wellknown.LabelExternalHetJobId]
}
