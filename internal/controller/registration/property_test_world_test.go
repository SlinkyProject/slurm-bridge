// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/cache"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurminterceptor "github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	schedulercontrol "github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/controllercheck"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Effects retain the evidence at the mutation boundary, including the cache
// observed by a submission. Snapshots never read the current Slurm cache.
type registrationEffect struct {
	Operation                           string
	BeforePod, AfterPod                 *corev1.Pod
	JobID                               int32
	BoundPods                           []string
	BeforeJobs, Submitted, ObservedJobs []api.V0044JobInfo
}

type registrationState struct {
	Pods        []corev1.Pod
	Jobs        []api.V0044JobInfo
	Effects     []registrationEffect
	Allocations []worldAllocation
}

type registrationInformer struct {
	slurmclient.InformerCache
	synced bool
}

func (i *registrationInformer) HasSynced() (bool, error) { return i.synced, nil }

type registrationWorld struct {
	ctx                context.Context
	kube               client.Client
	live, cached       slurmclient.Client
	informer           *registrationInformer
	reconciler         *Reconciler
	queue              *controllercheck.Queue[Request]
	clock              *clocktesting.FakeClock
	faults             *controllercheck.Faults
	trace              *controllercheck.Trace
	observe            func()
	effects            []registrationEffect
	slots, incarnation int
	nextJob            int32
	workloadAPI        *slurmjobir.WorkloadAPI
	allocations        []worldAllocation
}

func newRegistrationWorld(ctx context.Context, slots int) (*registrationWorld, error) {
	w := &registrationWorld{ctx: ctx, slots: slots, nextJob: 100,
		clock: clocktesting.NewFakeClock(time.Unix(0, 0)), trace: &controllercheck.Trace{},
		live: slurmfake.NewFakeClient(), cached: slurmfake.NewFakeClient()}
	w.faults = &controllercheck.Faults{Trace: w.trace}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := lwsv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	var err error
	w.workloadAPI, err = slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
	if err != nil {
		return nil, err
	}
	w.kube = fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return w.faults.Call("pod.patch", func() error {
				before := &corev1.Pod{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), before); err != nil {
					return err
				}
				if err := c.Patch(ctx, obj, patch, opts...); err != nil {
					return err
				}
				after := &corev1.Pod{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), after); err != nil {
					return err
				}
				w.queue.Add(podRequest(after))
				w.record(registrationEffect{Operation: "pod.patch", BeforePod: before, AfterPod: after})
				return nil
			})
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			return w.faults.Call("pod.delete", func() error {
				if err := c.Delete(ctx, obj, opts...); err != nil {
					return err
				}
				pod := obj.(*corev1.Pod)
				w.queue.Add(podRequest(pod))
				w.record(registrationEffect{Operation: "pod.delete", BeforePod: pod.DeepCopy()})
				return nil
			})
		},
	}).Build()
	if err := w.restart(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *registrationWorld) record(e registrationEffect) {
	w.effects = append(w.effects, e)
	w.trace.Logf("commit: %s job=%d", e.Operation, e.JobID)
	if w.observe != nil {
		w.observe()
	}
}

func worldJobs(ctx context.Context, c slurmclient.Client) ([]api.V0044JobInfo, error) {
	list := &api.V0044JobInfoObjectList{}
	if err := c.List(ctx, list); err != nil {
		return nil, err
	}
	// The Slurm fake's List returns store pointers: snapshots must own their data.
	out := make([]api.V0044JobInfo, len(list.Items))
	for i := range list.Items {
		out[i] = *list.Items[i].DeepCopy()
	}
	slices.SortFunc(out, func(a, b api.V0044JobInfo) int { return cmp.Compare(*a.JobId, *b.JobId) })
	return out, nil
}

func (w *registrationWorld) snapshot(ctx context.Context) (registrationState, error) {
	pods := &corev1.PodList{}
	if err := w.kube.List(ctx, pods); err != nil {
		return registrationState{}, err
	}
	slices.SortFunc(pods.Items, func(a, b corev1.Pod) int { return cmp.Compare(a.Name, b.Name) })
	jobs, err := worldJobs(ctx, w.live)
	return registrationState{Pods: pods.Items, Jobs: jobs, Effects: slices.Clone(w.effects), Allocations: slices.Clone(w.allocations)}, err
}

func (w *registrationWorld) restart(ctx context.Context) error {
	w.queue = controllercheck.NewQueue(func(a, b Request) int { return cmp.Compare(a.String(), b.String()) })
	w.queue.Now = w.clock.Now().Sub(time.Unix(0, 0))
	w.cached = slurmfake.NewFakeClient()
	w.informer = &registrationInformer{InformerCache: w.cached.GetInformer(api.ObjectTypeV0044JobInfo)}
	if _, err := slurmjob.NewPodIndex(w.cached, w.informer); err != nil {
		return err
	}
	slurm := slurminterceptor.NewClient(w.live, slurminterceptor.Funcs{
		List: func(ctx context.Context, list object.ObjectList, opts ...slurmclient.ListOption) error {
			return w.cached.List(ctx, list, opts...)
		},
		Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
			if (&slurmclient.GetOptions{}).ApplyOptions(opts).SkipCache {
				return w.live.Get(ctx, key, obj, opts...)
			}
			return w.cached.Get(ctx, key, obj, opts...)
		},
		Delete: func(ctx context.Context, obj object.Object, _ ...slurmclient.DeleteOption) error {
			return w.faults.Call("slurm.cancel", func() error {
				before, err := worldJobs(ctx, w.live)
				if err != nil {
					return err
				}
				job := &api.V0044JobInfo{}
				if err := w.live.Get(ctx, obj.GetKey(), job); err != nil {
					return err
				}
				if err := w.transitionAllocation(ctx, worldLeader(*job), api.V0044JobInfoJobStateCANCELLED); err != nil {
					return err
				}
				w.record(registrationEffect{Operation: "slurm.cancel", JobID: *job.JobId, BeforeJobs: before})
				return nil
			})
		},
	})
	w.reconciler = &Reconciler{Client: w.kube, slurmClient: slurm, informer: w.informer,
		schedulerName: "slurm-bridge", workloadAPI: w.workloadAPI, submit: w, recent: cache.NewExpiringWithClock(w.clock), enqueue: w.queue.Add}
	// Model startup lists, including the initial synchronized Slurm list. A
	// restarted process cannot advertise an empty, synchronized informer.
	pods := &corev1.PodList{}
	if err := w.kube.List(ctx, pods); err != nil {
		return err
	}
	for i := range pods.Items {
		w.queue.Add(podRequest(&pods.Items[i]))
	}
	return w.advanceCache(ctx)
}

func (w *registrationWorld) advanceCache(ctx context.Context) error {
	jobs, err := worldJobs(ctx, w.live)
	if err != nil {
		return err
	}
	for i := range jobs {
		old := &api.V0044JobInfo{}
		if err := w.cached.Get(ctx, jobs[i].GetKey(), old); err != nil {
			if err := w.cached.Create(ctx, &jobs[i], nil); err != nil {
				return err
			}
		} else {
			enqueueJob(old, w.queue.Add)
			if err := w.cached.Update(ctx, &jobs[i], nil); err != nil {
				return err
			}
		}
		enqueueJob(&jobs[i], w.queue.Add)
	}
	w.informer.synced = true
	w.trace.Logf("cache advanced: %d records", len(jobs))
	return nil
}

// RegisterJob models Slurm's atomic held submission, including a committed
// submission whose reply is lost. Identity encoding is the real write path.
func (w *registrationWorld) RegisterJob(ctx context.Context, ir *slurmjobir.SlurmJobIR) (int32, error) {
	var id int32
	err := w.faults.Call("slurm.submit", func() error {
		observed, err := worldJobs(ctx, w.cached)
		if err != nil {
			return err
		}
		pods := &corev1.PodList{}
		if err := w.kube.List(ctx, pods); err != nil {
			return err
		}
		var bound []string
		for _, pod := range pods.Items {
			if pod.Spec.NodeName != "" {
				bound = append(bound, externaljobinfo.PodKey(&pod))
			}
		}
		id = w.nextJob
		w.nextJob += 2 * int32(len(ir.Components)) //nolint:gosec // Translation limits the IR to 128 components.
		// Component IDs deliberately differ from leader + offset.
		var submitted []api.V0044JobInfo
		for i, component := range ir.Components {
			info := schedulercontrol.ExternalJobInfoFor(ir.RootPOM, component)
			job := api.V0044JobInfo{JobId: ptr.To(id + 2*int32(i)), AdminComment: ptr.To(info.ToString()),
				JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
				Priority: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(0))}}
			if len(ir.Components) > 1 {
				job.HetJobId = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(id)}
				job.HetJobOffset = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(i))}
			}
			if err := w.live.Create(ctx, &job, nil); err != nil {
				return err
			}
			submitted = append(submitted, job)
		}
		w.record(registrationEffect{Operation: "slurm.submit", JobID: id, Submitted: submitted, ObservedJobs: observed, BoundPods: bound})
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (w *registrationWorld) advanceTime(d time.Duration) {
	w.clock.Step(d)
	w.queue.Now += d
}

func (w *registrationWorld) process(ctx context.Context, key Request) error {
	w.queue.Take(key)
	w.trace.Logf("reconcile: %s time=%s synced=%t", key, w.queue.Now, w.informer.synced)
	result, err := w.reconciler.Reconcile(ctx, key)
	if err != nil {
		w.queue.AddRateLimited(key)
	} else {
		w.queue.Forget(key)
		if result.RequeueAfter > 0 {
			w.queue.AddAfter(key, result.RequeueAfter)
		}
	}
	w.trace.Logf("result: %+v error=%v", result, err)
	return err
}

func (w *registrationWorld) changeJob(rt *rapid.T, state api.V0044JobInfoJobState) error {
	jobs, err := worldJobs(w.ctx, w.live)
	if err != nil {
		return err
	}
	jobs = slices.DeleteFunc(jobs, func(j api.V0044JobInfo) bool { return !worldUnfinished(j) })
	if len(jobs) == 0 {
		rt.Skip("no unfinished jobs")
	}
	job := jobs[rapid.IntRange(0, len(jobs)-1).Draw(rt, "job-index")]
	return w.transitionAllocation(w.ctx, worldLeader(job), state)
}

// A competing writer or a replayed submission can leave a second allocation
// in Slurm. Generate that external state directly, without counting it as a
// submission by the reconciler under test.
func (w *registrationWorld) duplicateAllocation(rt *rapid.T) error {
	jobs, err := worldJobs(w.ctx, w.live)
	if err != nil {
		return err
	}
	jobs = slices.DeleteFunc(jobs, func(job api.V0044JobInfo) bool { return !worldUnfinished(job) })
	if len(jobs) == 0 {
		rt.Skip("no unfinished allocations")
	}
	leader := worldLeader(jobs[rapid.IntRange(0, len(jobs)-1).Draw(rt, "allocation-index")])
	replacement := w.nextJob
	for _, job := range jobs {
		if worldLeader(job) != leader {
			continue
		}
		job.JobId = ptr.To(w.nextJob)
		w.nextJob += 2
		if job.HetJobId != nil {
			job.HetJobId.Number = ptr.To(replacement)
		}
		job.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}
		job.Priority.Number = ptr.To(int32(0))
		if err := w.live.Create(w.ctx, &job, nil); err != nil {
			return err
		}
	}
	w.trace.Logf("external duplicate: allocation %d -> %d", leader, replacement)
	return nil
}

// A pre-upgrade native gang has a Pod UID list but no group identity. Composite
// groups had no released legacy format. Inject authoritative records only; the
// usual cache-advance action determines when the controller discovers them.
// The old scheduler submitted released pending jobs, rather than held ones.
func (w *registrationWorld) legacyAllocation(rt *rapid.T) error {
	s, err := w.snapshot(w.ctx)
	if err != nil {
		return err
	}
	var candidates []worldAllocation
	for _, allocation := range s.Allocations {
		if !strings.HasPrefix(allocation.Key, "scheduling.k8s.io/PodGroup/") {
			continue
		}
		complete := true
		for _, key := range allocation.Components[0] {
			if !slices.ContainsFunc(s.Pods, func(p corev1.Pod) bool {
				return externaljobinfo.PodKey(&p) == key && p.Spec.NodeName == "" && p.DeletionTimestamp == nil
			}) {
				complete = false
			}
		}
		if complete {
			candidates = append(candidates, allocation)
		}
	}
	if len(candidates) == 0 {
		rt.Skip("no complete unbound PodGroup")
	}
	allocation := candidates[rapid.IntRange(0, len(candidates)-1).Draw(rt, "gang-index")]
	info := externaljobinfo.ExternalJobInfo{Pods: slices.Clone(allocation.Components[0])}
	job := &api.V0044JobInfo{JobId: ptr.To(w.nextJob), AdminComment: ptr.To(info.ToString()),
		JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
		Priority: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(100))}}
	w.nextJob += 2
	w.trace.Logf("legacy allocation: %d for %s", *job.JobId, allocation.Key)
	return w.live.Create(w.ctx, job, nil)
}

func (w *registrationWorld) bindPod(rt *rapid.T) error {
	s, err := w.snapshot(w.ctx)
	if err != nil {
		return err
	}
	var candidates []corev1.Pod
	for _, pod := range s.Pods {
		if pod.Spec.NodeName != "" || pod.DeletionTimestamp != nil || len(pod.Spec.SchedulingGates) != 0 {
			continue
		}
		for _, job := range s.Jobs {
			if pod.Labels[wellknown.LabelExternalJobId] == strconv.FormatInt(int64(*job.JobId), 10) && job.JobState != nil && slices.Contains(*job.JobState, api.V0044JobInfoJobStateRUNNING) && slices.Contains(strings.Split(ptr.Deref(job.Nodes, ""), ","), "node-"+pod.Name) {
				candidates = append(candidates, pod)
			}
		}
	}
	if len(candidates) == 0 {
		rt.Skip("no ungated Pod with a running allocation")
	}
	pod := candidates[rapid.IntRange(0, len(candidates)-1).Draw(rt, "bind-index")]
	before := pod.DeepCopy()
	pod.Spec.NodeName = "node-" + pod.Name
	// This is an external binding, not a registration-controller patch. Bind one
	// member, preserving partial gangs across cache advances and restarts.
	if err := w.kube.Update(w.ctx, &pod); err != nil {
		return err
	}
	w.queue.Add(podRequest(&pod))
	w.record(registrationEffect{Operation: "pod.bind", BeforePod: before, AfterPod: pod.DeepCopy()})
	return nil
}

// Slurm starts and cancels a heterogeneous allocation as one unit. Sibling
// components are not independent allocations or duplicate survivors.
func (w *registrationWorld) transitionAllocation(ctx context.Context, leader int32, state api.V0044JobInfoJobState) error {
	jobs, err := worldJobs(ctx, w.live)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if worldLeader(job) != leader {
			continue
		}
		job.JobState = &[]api.V0044JobInfoJobState{state}
		if state == api.V0044JobInfoJobStateRUNNING {
			job.Priority.Number = ptr.To(int32(100))
			info, err := worldIdentity(job)
			if err != nil {
				return err
			}
			var nodes []string
			for _, key := range info.Pods {
				nodes = append(nodes, "node-"+externaljobinfo.ParsePodKey(key).Name)
			}
			job.Nodes = ptr.To(strings.Join(nodes, ","))
		}
		if err := w.live.Update(ctx, &job, nil); err != nil {
			return err
		}
	}
	w.trace.Logf("allocation %d -> %s", leader, state)
	return nil
}

func (w *registrationWorld) campaign() controllercheck.Campaign[registrationState] {
	return controllercheck.Campaign[registrationState]{
		Snapshot: w.snapshot, Trace: w.trace, ObserveEffects: func(f func()) { w.observe = f },
		Actions: map[string]func(*rapid.T) error{
			"create-workload":      w.create,
			"duplicate-allocation": w.duplicateAllocation,
			"legacy-allocation":    w.legacyAllocation,
			"bind-pod":             w.bindPod,
			"delete-pod": func(rt *rapid.T) error {
				s, err := w.snapshot(w.ctx)
				if err != nil {
					return err
				}
				if len(s.Pods) == 0 {
					rt.Skip("no Pods")
				}
				pod := s.Pods[rapid.IntRange(0, len(s.Pods)-1).Draw(rt, "pod-index")]
				return w.kube.Delete(w.ctx, &pod)
			},
			"advance-cache": func(*rapid.T) error { return w.advanceCache(w.ctx) },
			"process-key": func(rt *rapid.T) error {
				ready := w.queue.Ready()
				if len(ready) == 0 {
					rt.Skip("no ready keys")
				}
				return w.process(w.ctx, rapid.SampledFrom(ready).Draw(rt, "key"))
			},
			"advance-time": func(rt *rapid.T) error {
				w.advanceTime(time.Duration(rapid.IntRange(1, 90).Draw(rt, "seconds")) * time.Second)
				return nil
			},
			"cancel-job": func(rt *rapid.T) error { return w.changeJob(rt, api.V0044JobInfoJobStateCANCELLED) },
			"start-job":  func(rt *rapid.T) error { return w.changeJob(rt, api.V0044JobInfoJobStateRUNNING) },
			"arm-fault": func(rt *rapid.T) error {
				op := rapid.SampledFrom([]string{"pod.patch", "pod.delete", "slurm.submit", "slurm.cancel"}).Draw(rt, "operation")
				mode := rapid.SampledFrom([]controllercheck.FailureMode{controllercheck.BeforeCommit, controllercheck.AfterCommit}).Draw(rt, "mode")
				w.trace.Logf("arm: %s %s", op, mode)
				w.faults.Arm(op, mode)
				return nil
			},
			"heal":           func(*rapid.T) error { w.faults.Heal(); return nil },
			"restart":        func(*rapid.T) error { return w.restart(w.ctx) },
			"cache-unsynced": func(*rapid.T) error { w.informer.synced = false; return nil },
		},
		Recover: func(ctx context.Context) error { w.faults.Heal(); return w.advanceCache(ctx) },
		Progress: func(ctx context.Context) error {
			if err := w.advanceCache(ctx); err != nil {
				return err
			}
			w.advanceTime(registrationCooldown + time.Second)
			// One fair pass, including keys queued by publication on the next tick.
			for _, key := range w.queue.Ready() {
				if err := w.process(ctx, key); err != nil {
					return err
				}
			}
			return nil
		},
		Restart: w.restart, Retryable: func(err error) bool { return errors.Is(err, controllercheck.ErrInjected) }, RecoverySteps: 32,
	}
}
