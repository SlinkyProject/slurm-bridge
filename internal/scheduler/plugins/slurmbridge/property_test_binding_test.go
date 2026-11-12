// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/controllercheck"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Exercise real PreFilter/PreBind hooks across changing live Slurm observations,
// member replacement, and fresh framework cycles. Binding validation is shared
// within an attempt: later external changes are detected on the NEXT attempt,
// not atomically with every Pod binding. Identity and node checks remain per Pod.
// Claim reads mark entry into claim management; rejected bindings must never
// reach that boundary. Device allocation/publication has separate DRA tests.
func TestSchedulerBindingLifecycleProperties(t *testing.T) {
	t.Parallel()
	controllercheck.ExploreParallel(t, bindingPropertyContract, func(rt *rapid.T) controllercheck.Campaign[bindingPropertyState] {
		w := newBindingPropertyWorld(rt, rapid.Int32Range(2, 6).Draw(rt, "members"))
		w.retry(rt)
		// Preserve the original failure/heal/retry/success/changed-allocation episode.
		fault := rapid.SampledFrom(bindingPropertyFaults).Draw(rt, "initial-fault")
		w.setFault(fault)
		w.bind(rt, 0, "unchanged")
		w.setFault("healthy")
		w.bind(rt, 1, "unchanged")
		w.retry(rt)
		w.bind(rt, 1, "unchanged")
		w.setFault(fault)
		w.retry(rt)
		w.bind(rt, 0, "unchanged")
		member := func(rt *rapid.T) int { return rapid.IntRange(0, len(w.pods)-1).Draw(rt, "member") }
		progress := func(context.Context) error {
			for _, i := range rapid.Permutation(schedulingPropertyIndices(len(w.pods))).Draw(rt, "recovery-order") {
				w.bind(rt, i, "unchanged")
			}
			return nil
		}
		return controllercheck.Campaign[bindingPropertyState]{
			Trace:          &w.trace,
			Snapshot:       func(context.Context) (bindingPropertyState, error) { return w.snapshot(), nil },
			ObserveEffects: func(observe func()) { w.observe = observe },
			Actions: map[string]func(*rapid.T) error{
				"fault": func(rt *rapid.T) error {
					w.setFault(rapid.SampledFrom(bindingPropertyFaults).Draw(rt, "fault"))
					return nil
				},
				"heal":  func(*rapid.T) error { w.setFault("healthy"); return nil },
				"retry": func(rt *rapid.T) error { w.retry(rt); return nil },
				"bind": func(rt *rapid.T) error {
					w.bind(rt, member(rt), rapid.SampledFrom([]string{"unchanged", "unchanged", "uid", "job", "hetjob", "node"}).Draw(rt, "binding-input"))
					return nil
				},
				"replace": func(rt *rapid.T) error {
					i := member(rt)
					old := w.pods[i]
					if err := w.sb.Delete(rt.Context(), old); err != nil {
						return err
					}
					replacement := old.DeepCopy()
					w.generation++
					replacement.UID = types.UID(fmt.Sprintf("replacement-%d", w.generation))
					replacement.ResourceVersion = ""
					if err := w.sb.Create(rt.Context(), replacement); err != nil {
						return err
					}
					w.pods[i] = replacement
					w.bind(rt, i, "unchanged") // Explicitly exercise the now-stale cycle.
					return nil
				},
			},
			Recover:  func(context.Context) error { w.setFault("healthy"); w.retry(rt); return nil },
			Progress: progress,
			Restart: func(ctx context.Context) error {
				w.sb = &SlurmBridge{Client: w.sb.Client, workloadAPI: w.sb.workloadAPI, slurmControl: w.control, handle: w.sb.handle, kubeNodeIndex: w.sb.kubeNodeIndex}
				w.retry(rt)
				return progress(ctx)
			},
			RecoverySteps: 2,
		}
	})
}

type bindingPropertyEffect struct {
	want, success   bool
	claims          int
	live, resources int32
}
type bindingPropertyAttempt struct {
	fresh bool
	reads int32
}
type bindingPropertyState struct {
	effects  []bindingPropertyEffect
	attempts []bindingPropertyAttempt
	complete bool
}

var bindingPropertyContract = controllercheck.Contract[bindingPropertyState]{
	Always: []controllercheck.Rule[bindingPropertyState]{
		{Name: "binding-decision", Statement: "binding uses the attempt's validation and each member's pinned identity/node", Check: func(s bindingPropertyState) error {
			for _, e := range s.effects {
				if e.success != e.want {
					return fmt.Errorf("binding success=%t, want %t", e.success, e.want)
				}
			}
			return nil
		}},
		{Name: "claim-management", Statement: "only valid bindings enter claim management, once per binding", Check: func(s bindingPropertyState) error {
			for _, e := range s.effects {
				want := 0
				if e.want {
					want = 1
				}
				if e.claims != want {
					return fmt.Errorf("claim reads=%d, want %d", e.claims, want)
				}
			}
			return nil
		}},
		{Name: "attempt-isolation", Statement: "each attempt refreshes its snapshot and shares binding validation only within that attempt", Check: func(s bindingPropertyState) error {
			for _, a := range s.attempts {
				if !a.fresh || a.reads != 1 {
					return fmt.Errorf("attempt fresh=%t allocation reads=%d", a.fresh, a.reads)
				}
			}
			for _, e := range s.effects {
				if e.live > 1 || e.resources > 1 {
					return fmt.Errorf("shared reads repeated: live=%d resources=%d", e.live, e.resources)
				}
			}
			return nil
		}},
	},
	Eventually: []controllercheck.Rule[bindingPropertyState]{
		{Name: "binding-recovers", Statement: "after faults stop, every current member can bind in a fresh attempt", Check: func(s bindingPropertyState) error {
			if !s.complete {
				return fmt.Errorf("not all current members can bind")
			}
			return nil
		}},
	},
}

func (w *bindingPropertyWorld) snapshot() bindingPropertyState {
	complete := len(w.bound) == len(w.pods)
	for _, bound := range w.bound {
		complete = complete && bound
	}
	return bindingPropertyState{effects: w.effects, attempts: w.attempts, complete: complete}
}

var bindingPropertyFaults = []string{"job", "hetjob", "pending", "finished", "lost-node", "live-read", "resource-read"}

type bindingPropertyWorld struct {
	trace      controllercheck.Trace
	observe    func()
	generation int
	effects    []bindingPropertyEffect
	attempts   []bindingPropertyAttempt
	bound      []bool
	sb         *SlurmBridge
	control    *nativeStateControl
	pods       []*corev1.Pod
	states     []*framework.CycleState
	nodes      []string
	pinnedUIDs []types.UID
	fault      string
	// The oracle records the environment at the first valid binding attempt,
	// independently of the plugin's internal validation flags/status objects.
	checked       bool
	accepted      bool
	claimReads    int
	liveReads     int32
	resourceReads int32
}

func (w *bindingPropertyWorld) setFault(fault string) {
	w.fault = fault
	w.control.liveErr, w.control.resourceErr = nil, nil
	live := w.control.job
	switch fault {
	case "job":
		live.JobId++
	case "hetjob":
		live.HetJobId++
	case "pending":
		live.Pending = true
	case "finished":
		live.Finished = true
	case "lost-node":
		live.Nodes = "node1"
	case "live-read":
		w.control.liveErr = errors.New("injected live read failure")
	case "resource-read":
		w.control.resourceErr = errors.New("injected resource read failure")
	}
	w.control.liveJob = &live
}

func (w *bindingPropertyWorld) retry(rt *rapid.T) {
	var previous *nativeAllocation
	if len(w.states) > 0 {
		state, err := getStateData(w.states[0])
		if err != nil {
			rt.Fatal(err)
		}
		previous = state.native
	}
	reads := w.control.allocationReads.Load()
	group := framework.NewCycleState()
	w.states = make([]*framework.CycleState, len(w.pods))
	w.nodes = make([]string, len(w.pods))
	w.pinnedUIDs = make([]types.UID, len(w.pods))
	w.bound = make([]bool, len(w.pods))
	fresh := true
	for _, i := range rapid.Permutation(schedulingPropertyIndices(len(w.pods))).Draw(rt, "prefilter-order") {
		w.states[i] = nativePodState(group)
		result, status := w.sb.PreFilter(rt.Context(), w.states[i], w.pods[i], nil)
		if !status.IsSuccess() || result == nil || len(result.NodeNames) != 1 {
			rt.Fatalf("PreFilter: result=%v status=%v", result, status)
		}
		for node := range result.NodeNames {
			w.nodes[i] = node
		}
		state, err := getStateData(w.states[i])
		if err != nil {
			rt.Fatal(err)
		}
		fresh = fresh && state.native != previous
		w.pinnedUIDs[i] = w.pods[i].UID
	}
	w.attempts = append(w.attempts, bindingPropertyAttempt{fresh: fresh, reads: w.control.allocationReads.Load() - reads})
	w.checked, w.accepted = false, false
	w.liveReads, w.resourceReads = w.control.liveReads.Load(), w.control.resourceReads.Load()
}

func (w *bindingPropertyWorld) bind(rt *rapid.T, i int, change string) {
	pod := w.pods[i].DeepCopy()
	node := w.nodes[i]
	switch change {
	case "uid":
		pod.UID += "-changed"
	case "job":
		pod.Labels[wellknown.LabelExternalJobId] = "11"
	case "hetjob":
		pod.Labels[wellknown.LabelExternalHetJobId] = "11"
	case "node":
		node = "node1"
		if node == w.nodes[i] {
			node = "node2"
		}
	}
	validMember := change == "unchanged" && pod.UID == w.pinnedUIDs[i]
	if validMember && !w.checked {
		w.checked, w.accepted = true, w.fault == "healthy"
	}
	want := validMember && w.accepted
	before := w.claimReads
	status := w.sb.PreBind(rt.Context(), w.states[i], pod, node)
	w.effects = append(w.effects, bindingPropertyEffect{want: want, success: status.IsSuccess(), claims: w.claimReads - before, live: w.control.liveReads.Load() - w.liveReads, resources: w.control.resourceReads.Load() - w.resourceReads})
	w.bound[i] = status.IsSuccess()
	w.trace.Logf("bind: member=%d change=%s fault=%s want=%t status=%v", i, change, w.fault, want, status)
	if w.observe != nil {
		w.observe()
	}
}

func newBindingPropertyWorld(rt *rapid.T, count int32) *bindingPropertyWorld {
	scheme, err := newClientScheme()
	if err != nil {
		rt.Fatal(err)
	}
	api, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
	if err != nil {
		rt.Fatal(err)
	}
	pg := &slurmjobir.PodGroup{TypeMeta: api.PodGroupTypeMeta, ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "gang", UID: "gang"}, Spec: slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: count}}}}
	w := &bindingPropertyWorld{fault: "healthy"}
	objects := []client.Object{pg}
	nodes := make([]*corev1.Node, count)
	for i := range count {
		pod := allocationPod(fmt.Sprintf("pod-%d", i), 0, "10", "")
		pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To("gang")}
		w.pods = append(w.pods, pod)
		nodes[i] = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node%d", i+1)}}
		objects = append(objects, pod.DeepCopy(), nodes[i])
	}
	kube := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(pg).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*resourcev1.ResourceClaim); ok {
				w.claimReads++
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	factory := informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0)
	index, err := newKubeNodeNameIndex(factory.Core().V1().Nodes().Informer())
	if err != nil {
		rt.Fatal(err)
	}
	for _, node := range nodes {
		if err := index.index.Add(node); err != nil {
			rt.Fatal(err)
		}
	}
	index.hasSynced = func() bool { return true }
	handle, err := fwkruntime.NewFramework(rt.Context(), nil, nil, fwkruntime.WithClientSet(clientsetfake.NewClientset()), fwkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(nil, nodes)), fwkruntime.WithPodActivator(&activateRecorder{}))
	if err != nil {
		rt.Fatal(err)
	}
	rt.Cleanup(func() { _ = handle.Close() })
	w.control = &nativeStateControl{job: slurmcontrol.ExternalJob{JobId: 10, Nodes: fmt.Sprintf("node[1-%d]", count)}}
	w.sb = &SlurmBridge{Client: kube, workloadAPI: api, slurmControl: w.control, handle: handle, kubeNodeIndex: index}
	return w
}
