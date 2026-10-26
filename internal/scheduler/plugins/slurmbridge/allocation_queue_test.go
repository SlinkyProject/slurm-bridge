// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingalpha "k8s.io/api/scheduling/v1alpha3"
	scheduling "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler/backend/queue"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	"k8s.io/utils/ptr"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	kubeinterceptor "sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type allocationTestHandle struct {
	fwk.Handle
	activate func(klog.Logger, map[string]*corev1.Pod)
	factory  informers.SharedInformerFactory
}

func (h *allocationTestHandle) Activate(logger klog.Logger, pods map[string]*corev1.Pod) {
	h.activate(logger, pods)
}

func (h *allocationTestHandle) SharedInformerFactory() informers.SharedInformerFactory {
	return h.factory
}

func allocationPod(name string, priority int32, jobID, ready string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name), Labels: map[string]string{
			wellknown.LabelExternalJobId: jobID, wellknown.LabelAllocatedJobId: ready,
		}},
		Spec: corev1.PodSpec{SchedulerName: "slurm-bridge", Priority: ptr.To(priority)},
	}
}

func queuedAllocationPod(pod *corev1.Pod, at time.Time) *framework.QueuedPodInfo {
	info, _ := framework.NewPodInfo(pod)
	return &framework.QueuedPodInfo{PodInfo: info, QueueingParams: framework.QueueingParams{Timestamp: at}}
}

func TestAllocationQueueLess(t *testing.T) {
	now := time.Now()
	sb := &SlurmBridge{}
	for _, tc := range []struct {
		name string
		a, b *corev1.Pod
	}{
		{"allocated beats priority", allocationPod("a", 0, "10", "10"), allocationPod("b", 500, "11", "")},
		{"ready retains priority", allocationPod("a", 500, "10", "10"), allocationPod("b", 0, "11", "11")},
		{"unallocated retains priority", allocationPod("a", 500, "", ""), allocationPod("b", 0, "", "")},
		{"stale hint ignored", allocationPod("a", 500, "11", ""), allocationPod("b", 0, "10", "9")},
		{"invalid identity ignored", allocationPod("a", 500, "11", ""), allocationPod("b", 0, "0", "0")},
		{"missing identity ignored", allocationPod("a", 500, "11", ""), allocationPod("b", 0, "", "10")},
		{"equal priority uses timestamp", allocationPod("a", 0, "10", "10"), allocationPod("b", 0, "11", "11")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := queuedAllocationPod(tc.a, now), queuedAllocationPod(tc.b, now.Add(time.Second))
			group := func(p *framework.QueuedPodInfo) *framework.QueuedPodGroupInfo {
				return &framework.QueuedPodGroupInfo{
					PodGroupInfo: &framework.PodGroupInfo{PodGroup: &scheduling.PodGroup{
						Spec: scheduling.PodGroupSpec{Priority: p.Pod.Spec.Priority},
					}},
					QueueingParams: p.QueueingParams,
					QueuedPodInfos: map[fwk.EntityKey][]*framework.QueuedPodInfo{
						fwk.PodGroupKey("test", "group"): {queuedAllocationPod(allocationPod("waiting", 0, "", ""), now), p},
					},
				}
			}
			composite := func(p *framework.QueuedPodInfo) *framework.QueuedPodGroupInfo {
				g := group(p)
				g.PodGroupInfo = &framework.PodGroupInfo{CompositePodGroup: &schedulingalpha.CompositePodGroup{
					Spec: schedulingalpha.CompositePodGroupSpec{Priority: p.Pod.Spec.Priority},
				}}
				return g
			}
			for _, left := range []fwk.QueuedEntityInfo{a, group(a), composite(a)} {
				for _, right := range []fwk.QueuedEntityInfo{b, group(b), composite(b)} {
					if !sb.Less(left, right) || sb.Less(right, left) || sb.Less(left, left) {
						t.Fatalf("incorrect allocation/priority/timestamp ordering for %T and %T", left, right)
					}
				}
			}
		})
	}
}

// Exercise the actual upstream queue: a low-priority gang receives its Slurm
// allocation after entering the queue, with higher-priority work already present.
// Informer handlers run independently, so check both update/activation orders.
func TestAllocationChangePromotesAlreadyQueuedGang(t *testing.T) {
	metrics.Register()
	for _, location := range []string{"active", "backoff", "unschedulable"} {
		for _, activateFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/activate-first=%t", location, activateFirst), func(t *testing.T) {
				ctx := t.Context()
				logger := klog.FromContext(ctx)
				factory := informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0)
				sb := &SlurmBridge{schedulerName: "slurm-bridge"}
				q := queue.NewPriorityQueue(sb.Less, factory,
					queue.WithMetricsRecorder(metrics.NewMetricsAsyncRecorder(1000, time.Second, ctx.Done())),
					queue.WithPodInitialBackoffDuration(time.Hour), queue.WithPodMaxBackoffDuration(time.Hour))
				t.Cleanup(q.Close)
				sb.handle = &allocationTestHandle{activate: q.Activate}
				o := &allocationObserver{ctx: ctx, bridge: sb, queue: allocationTestQueue(t)}
				var members []*corev1.Pod
				for i := range 48 {
					pod := allocationPod(fmt.Sprintf("gang-%02d", i), 0, "10", "")
					members = append(members, pod)
					q.Add(ctx, pod)
					if location != "active" {
						entity, err := q.Pop(logger)
						if err != nil {
							t.Fatal(err)
						}
						p := entity.(*framework.QueuedPodInfo)
						if location == "unschedulable" {
							p.UnschedulablePlugins = sets.New(Name)
						}
						if err := q.AddUnschedulablePodIfNotPresent(logger, p, q.SchedulingCycle()); err != nil {
							t.Fatal(err)
						}
					}
				}
				for i := range 64 {
					q.Add(ctx, allocationPod(fmt.Sprintf("high-%02d", i), 500, "11", ""))
				}
				for _, pod := range members {
					updated := pod.DeepCopy()
					updated.Labels[wellknown.LabelAllocatedJobId] = "10"
					activate := func() { o.podUpdated(pod, updated) }
					if activateFirst {
						activate()
					}
					q.Update(ctx, pod, updated)
					if !activateFirst {
						activate()
					}
				}
				for range 48 {
					entity, err := q.Pop(logger)
					if err != nil {
						t.Fatal(err)
					}
					p := entity.(*framework.QueuedPodInfo)
					q.Done(p.Pod.UID)
					if allocatedJobID(p.Pod) != "10" {
						t.Fatalf("selected %s ahead of allocated gang", p.Pod.Name)
					}
				}
			})
		}
	}
}

func TestAllocationObserverOnlyActivatesTransitions(t *testing.T) {
	calls := 0
	sb := &SlurmBridge{schedulerName: "slurm-bridge", handle: &allocationTestHandle{
		activate: func(klog.Logger, map[string]*corev1.Pod) { calls++ },
	}}
	o := &allocationObserver{ctx: t.Context(), bridge: sb, queue: allocationTestQueue(t)}
	old := allocationPod("pod", 0, "10", "")
	ready := allocationPod("pod", 0, "10", "10")
	o.podUpdated(old, ready)
	if calls != 1 {
		t.Fatal("allocation transition did not wake pod")
	}
	o.podUpdated(ready, ready.DeepCopy())
	o.podUpdated(ready, old)
	stale := allocationPod("pod", 0, "11", "10")
	o.podUpdated(old, stale)
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Spec.NodeName = "node" },
		func(p *corev1.Pod) { p.Spec.SchedulerName = "other" },
		func(p *corev1.Pod) { p.DeletionTimestamp = ptr.To(metav1.Now()) },
		func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed },
	} {
		p := ready.DeepCopy()
		mutate(p)
		o.podUpdated(old, p)
	}
	if calls != 1 {
		t.Fatalf("unexpected repeated or irrelevant wakeups: %d", calls)
	}
}

func TestAllocationLossRepairsActiveQueue(t *testing.T) {
	metrics.Register()
	ctx := t.Context()
	logger := klog.FromContext(ctx)
	sb := &SlurmBridge{}
	q := queue.NewPriorityQueue(sb.Less, informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0),
		queue.WithMetricsRecorder(metrics.NewMetricsAsyncRecorder(1000, time.Second, ctx.Done())))
	t.Cleanup(q.Close)
	low, high := allocationPod("low", 0, "10", "10"), allocationPod("high", 500, "11", "")
	for _, p := range []*corev1.Pod{low, high} {
		q.Add(ctx, p)
	}
	updated := low.DeepCopy()
	delete(updated.Labels, wellknown.LabelAllocatedJobId)
	q.Update(ctx, low, updated)
	entity, err := q.Pop(logger)
	if err != nil {
		t.Fatal(err)
	}
	p := entity.(*framework.QueuedPodInfo)
	q.Done(p.Pod.UID)
	if p.Pod.Name != "high" {
		t.Fatalf("stale allocation retained priority: %s", p.Pod.Name)
	}
}

func TestAllocationObserverReconcile(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		state                        api.V0044JobInfoJobState
		nodes, label, marker, member string
		bound, readError, conflict   bool
		want                         string
	}{
		{name: "pending registration", state: api.V0044JobInfoJobStatePENDING, label: "10", member: "test/pod"},
		{name: "UID identity", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", label: "10", member: "test/pod/pod", want: "10"},
		{name: "recreated Pod cannot inherit readiness", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", label: "10", marker: "10", member: "test/pod/old-uid"},
		{name: "running allocation", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", label: "10", member: "test/pod", want: "10"},
		{name: "idempotent observation", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", label: "10", marker: "10", member: "test/pod", want: "10"},
		{name: "lost allocation", state: api.V0044JobInfoJobStatePENDING, label: "10", marker: "10", member: "test/pod"},
		{name: "canceled job retains historical nodes", state: api.V0044JobInfoJobStateCANCELLED, nodes: "node1", label: "10", marker: "10", member: "test/pod"},
		{name: "completing job", state: api.V0044JobInfoJobStateCOMPLETING, nodes: "node1", label: "10", marker: "10", member: "test/pod"},
		{name: "suspended job", state: api.V0044JobInfoJobStateSUSPENDED, nodes: "node1", label: "10", marker: "10", member: "test/pod"},
		{name: "no nodes", state: api.V0044JobInfoJobStateRUNNING, label: "10", marker: "10", member: "test/pod"},
		{name: "replaced identity", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", label: "11", marker: "10", member: "test/pod"},
		{name: "missing identity", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", marker: "10", member: "test/pod"},
		{name: "other job member", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", label: "10", marker: "10", member: "test/other"},
		{name: "bound pod untouched", state: api.V0044JobInfoJobStateCANCELLED, nodes: "node1", label: "10", marker: "10", member: "test/pod", bound: true, want: "10"},
		{name: "read failure preserves last observation", label: "10", marker: "10", readError: true, want: "10"},
		{name: "stale snapshot cannot update replacement", state: api.V0044JobInfoJobStateRUNNING, nodes: "node1", label: "10", member: "test/pod", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pod := allocationPod("pod", 0, tc.label, tc.marker)
			if tc.bound {
				pod.Spec.NodeName = "node1"
			}
			kc := kubefake.NewFakeClient(pod)
			cached := &corev1.Pod{}
			if err := kc.Get(ctx, kubeclient.ObjectKeyFromObject(pod), cached); err != nil {
				t.Fatal(err)
			}
			beforeRV := cached.ResourceVersion
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			if err := indexer.Add(cached); err != nil {
				t.Fatal(err)
			}
			if tc.conflict {
				replaced := cached.DeepCopy()
				replaced.Labels[wellknown.LabelExternalJobId] = "20"
				if err := kc.Update(ctx, replaced); err != nil {
					t.Fatal(err)
				}
			}
			job := &api.V0044JobInfo{
				JobId: ptr.To(int32(10)), Nodes: ptr.To(tc.nodes), JobState: ptr.To([]api.V0044JobInfoJobState{tc.state}),
				AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{Pods: []string{tc.member}}).ToString()),
			}
			sc := slurmfake.NewClientBuilder().WithObjects(job).Build()
			if tc.readError {
				sc = interceptor.NewClient(sc, interceptor.Funcs{
					Get: func(context.Context, object.ObjectKey, object.Object, ...slurmclient.GetOption) error {
						return errors.New("Slurm unavailable")
					},
				})
			}
			o := &allocationObserver{ctx: ctx, bridge: &SlurmBridge{Client: kc, schedulerName: "slurm-bridge"}, jobs: &allocationTestInformer{reader: sc}, pods: corelisters.NewPodLister(indexer)}
			err := o.reconcile(ctx, kubeclient.ObjectKeyFromObject(pod))
			if (err != nil) != (tc.readError || tc.conflict) {
				t.Fatalf("reconcile error = %v", err)
			}
			got := &corev1.Pod{}
			if err := kc.Get(ctx, kubeclient.ObjectKeyFromObject(pod), got); err != nil {
				t.Fatal(err)
			}
			if got.Labels[wellknown.LabelAllocatedJobId] != tc.want {
				t.Fatalf("hint = %q, want %q", got.Labels[wellknown.LabelAllocatedJobId], tc.want)
			}
			if tc.want == tc.marker && !tc.conflict && got.ResourceVersion != beforeRV {
				t.Fatal("unchanged hint wrote the Pod")
			}
		})
	}
}

// Keep cache-reading tests independent of the fake client's informer lifecycle.
type allocationTestInformer struct {
	slurmclient.InformerCache
	reader slurmclient.Reader
}

func (i *allocationTestInformer) Get(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
	return i.reader.Get(ctx, key, obj, opts...)
}

func allocationTestQueue(t *testing.T) workqueue.TypedRateLimitingInterface[kubeclient.ObjectKey] {
	t.Helper()
	q := workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[kubeclient.ObjectKey](time.Millisecond, time.Second))
	t.Cleanup(q.ShutDown)
	return q
}

// Native groups have a separate retry path from individual Pods. Exercise the
// real 48-member error-backoff queue, including either informer handler order.
func TestAllocationActivatesNativeGangFromErrorBackoff(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.GenericWorkload, true)
	metrics.Register()
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.TopologyAwareWorkloadScheduling, true)
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.CompositePodGroup, true)
	for _, composite := range []bool{false, true} {
		for _, activateFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("composite=%t/activate-first=%t", composite, activateFirst), func(t *testing.T) {
				ctx := t.Context()
				logger := klog.FromContext(ctx)
				sb := &SlurmBridge{schedulerName: "slurm-bridge"}
				q := queue.NewPriorityQueue(sb.Less, informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0),
					queue.WithMetricsRecorder(metrics.NewMetricsAsyncRecorder(1000, time.Second, ctx.Done())),
					queue.WithPodInitialBackoffDuration(time.Hour), queue.WithPodMaxBackoffDuration(time.Hour))
				t.Cleanup(q.Close)
				pg := &scheduling.PodGroup{ObjectMeta: metav1.ObjectMeta{Name: "gang", Namespace: "test"},
					Spec: scheduling.PodGroupSpec{Priority: ptr.To(int32(0))}}
				if composite {
					pg.Spec.ParentCompositePodGroupName = ptr.To("root")
					q.AddCompositePodGroup(logger, &schedulingalpha.CompositePodGroup{
						ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: "test"},
						Spec:       schedulingalpha.CompositePodGroupSpec{Priority: ptr.To(int32(0))},
					})
				}
				q.AddPodGroup(logger, pg)
				var members []*corev1.Pod
				for i := range 48 {
					pod := allocationPod(fmt.Sprintf("member-%d", i), 0, "10", "")
					pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To("gang")}
					members = append(members, pod)
					q.Add(ctx, pod)
				}
				entity, err := q.Pop(logger)
				if err != nil {
					t.Fatal(err)
				}
				group, ok := entity.(*framework.QueuedPodGroupInfo)
				if !ok || group.Size() != 48 {
					t.Fatalf("expected 48-member gang, got %T", entity)
				}
				group.ForEachPodInfo(func(p *framework.QueuedPodInfo) bool {
					if err := q.AddUnschedulablePodIfNotPresent(logger, p, q.SchedulingCycle()); err != nil {
						t.Fatal(err)
					}
					return true
				})
				if err := q.AddAttemptedPodGroupIfNeeded(logger, group, q.SchedulingCycle(), fwk.NewStatus(fwk.Error, "Slurm unavailable")); err != nil {
					t.Fatal(err)
				}
				if got := len(q.PodsInBackoffQ()); got != 48 {
					t.Fatalf("backoff contains %d pods", got)
				}
				q.Add(ctx, allocationPod("high-priority", 500, "11", ""))
				sb.handle = &allocationTestHandle{activate: q.Activate}
				o := &allocationObserver{ctx: ctx, bridge: sb, queue: allocationTestQueue(t)}
				for _, pod := range members {
					updated := pod.DeepCopy()
					updated.Labels[wellknown.LabelAllocatedJobId] = "10"
					if activateFirst {
						o.podUpdated(pod, updated)
					}
					q.Update(ctx, pod, updated)
					if !activateFirst {
						o.podUpdated(pod, updated)
					}
				}
				if got := len(q.PodsInBackoffQ()); got != 0 {
					t.Fatalf("allocation left %d pods in backoff", got)
				}
				next, err := q.Pop(logger)
				if err != nil {
					t.Fatal(err)
				}
				ready, ok := next.(*framework.QueuedPodGroupInfo)
				if !ok || ready.Size() != 48 || !entityHasAllocation(ready) {
					t.Fatalf("allocated gang did not precede higher-priority work: %T", next)
				}
			})
		}
	}
}

// Drive the real Slurm informer and Kubernetes Pod informer, including initial
// allocations, late Pod registration, retries without another Slurm change,
// allocation loss, and deletion. No extra Slurm GETs or observer List loop.
func TestAllocationInformerLifecycle(t *testing.T) {
	ctx := t.Context()
	pod := allocationPod("pod", 0, "10", "")
	pod.ResourceVersion = "1"
	cs := clientsetfake.NewClientset(pod.DeepCopy())
	factory := informers.NewSharedInformerFactory(cs, 0)
	var failPatch atomic.Bool
	failPatch.Store(true)
	var attempts, activations atomic.Int32
	kc := kubefake.NewClientBuilder().WithObjects(pod).WithInterceptorFuncs(kubeinterceptor.Funcs{
		Patch: func(ctx context.Context, c kubeclient.WithWatch, obj kubeclient.Object, patch kubeclient.Patch, opts ...kubeclient.PatchOption) error {
			attempts.Add(1)
			if failPatch.Swap(false) {
				return errors.New("temporary Kubernetes failure")
			}
			if err := c.Patch(ctx, obj, patch, opts...); err != nil {
				return err
			}
			latest := &corev1.Pod{}
			if err := c.Get(ctx, kubeclient.ObjectKeyFromObject(obj), latest); err != nil {
				return err
			}
			_, err := cs.CoreV1().Pods(latest.Namespace).Update(ctx, latest, metav1.UpdateOptions{})
			return err
		},
	}).Build()
	var state atomic.Int32 // 0: running, 1: pending, 2: deleted, 3: unavailable
	var requests atomic.Int32
	transport := allocationTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/jobs/") && !strings.HasSuffix(r.URL.Path, "/jobs") {
			return nil, fmt.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		requests.Add(1)
		if state.Load() == 3 {
			return nil, errors.New("Slurm unavailable")
		}
		body := `{"jobs":[],"errors":[]}`
		if phase := state.Load(); phase != 2 {
			jobState, nodes := "RUNNING", "node1"
			if phase == 1 {
				jobState, nodes = "PENDING", ""
			}
			comment := (&externaljobinfo.ExternalJobInfo{Pods: []string{"test/pod/pod", "test/late/late"}}).ToString()
			body = fmt.Sprintf(`{"jobs":[{"job_id":10,"nodes":%q,"job_state":[%q],"admin_comment":%q}],"errors":[]}`, nodes, jobState, comment)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	sb := &SlurmBridge{Client: kc, schedulerName: "slurm-bridge", handle: &allocationTestHandle{factory: factory,
		activate: func(klog.Logger, map[string]*corev1.Pod) { activations.Add(1) }}}
	o, err := newAllocationObserver(ctx, sb, &slurmclient.Config{Server: "http://slurm.test", TokenProvider: token.StaticProvider("test"), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	factory.Start(ctx.Done())
	o.start()
	await := func(message string, condition func() bool) {
		t.Helper()
		if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 8*time.Second, true, func(context.Context) (bool, error) { return condition(), nil }); err != nil {
			t.Fatalf("%s: %v", message, err)
		}
	}
	hint := func(name, want string) bool {
		p, err := o.pods.Pods("test").Get(name)
		return err == nil && p.Labels[wellknown.LabelAllocatedJobId] == want
	}
	await("initial allocation and failed patch retry", func() bool { return hint("pod", "10") && activations.Load() == 1 })
	if attempts.Load() < 2 {
		t.Fatal("failed patch was not retried")
	}
	late := allocationPod("late", 0, "10", "")
	if err := kc.Create(ctx, late); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods("test").Create(ctx, late, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	await("late registration sees existing allocation", func() bool { return hint("late", "10") && activations.Load() == 2 })
	state.Store(3)
	await("failed refresh observed", func() bool { _, err := o.jobs.HasSynced(); return err != nil })
	if !hint("pod", "10") || !hint("late", "10") {
		t.Fatal("failed refresh cleared allocation")
	}
	state.Store(1)
	await("allocation loss clears hints", func() bool { return hint("pod", "") && hint("late", "") })
	if activations.Load() != 2 {
		t.Fatal("allocation loss reactivated pods")
	}
	state.Store(0)
	await("new allocation reactivates pods", func() bool { return hint("pod", "10") && hint("late", "10") && activations.Load() == 4 })
	state.Store(2)
	await("deleted job clears hints", func() bool { return hint("pod", "") && hint("late", "") })
	if activations.Load() != 4 {
		t.Fatal("deletion reactivated pods")
	}
	if requests.Load() == 0 {
		t.Fatal("Slurm informer did not refresh")
	}
}

// allocationTestTransport keeps the informer lifecycle test independent of a live Slurm server.
type allocationTestTransport func(*http.Request) (*http.Response, error)

func (f allocationTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
