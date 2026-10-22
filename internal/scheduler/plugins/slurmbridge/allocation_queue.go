// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmcache "github.com/SlinkyProject/slurm-client/pkg/cache"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	slurmutils "github.com/SlinkyProject/slurm-client/pkg/utils"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

var _ fwk.QueueSortPlugin = &SlurmBridge{}

// Less uses only the queued entity's Pod snapshots. Reading a changing Slurm cache here
// would silently invalidate the active-queue heap without repairing its order.
// Updating the label instead lets the scheduler's Pod update handler fix the heap.
func (sb *SlurmBridge) Less(a, b fwk.QueuedEntityInfo) bool {
	allocatedA := entityHasAllocation(a)
	allocatedB := entityHasAllocation(b)
	if allocatedA != allocatedB {
		return allocatedA
	}
	return (&queuesort.PrioritySort{}).Less(a, b)
}

func entityHasAllocation(entity fwk.QueuedEntityInfo) bool {
	queued, ok := entity.(framework.QueuedEntityInfo)
	if !ok {
		return false
	}
	ready := false
	queued.ForEachPodInfo(func(info *framework.QueuedPodInfo) bool {
		ready = allocatedJobID(info.Pod) != ""
		return !ready
	})
	return ready
}

func allocatedJobID(pod *corev1.Pod) string {
	id := pod.Labels[wellknown.LabelExternalJobId]
	if id == "" || pod.Labels[wellknown.LabelAllocatedJobId] != id {
		return ""
	}
	if n, err := strconv.ParseInt(id, 10, 32); err != nil || n <= 0 {
		return ""
	}
	return id
}

// allocationObserver discovers allocations even when the corresponding pods
// never win a scheduling attempt. Scheduling uses the same job informer for
// allocation discovery; destructive decisions retain explicit live validation.
type allocationObserver struct {
	ctx       context.Context
	once      sync.Once
	bridge    *SlurmBridge
	jobs      slurmclient.InformerCache
	pods      corelisters.PodLister
	podIndex  cache.Indexer
	hasSynced cache.InformerSynced
	queue     workqueue.TypedRateLimitingInterface[client.ObjectKey]
}

const allocationJobIndex = "slurmbridge-allocation-job"

func allocationJobIDs(obj interface{}) ([]string, error) {
	pod := obj.(*corev1.Pod)
	ids := []string{}
	for _, label := range []string{wellknown.LabelExternalJobId, wellknown.LabelAllocatedJobId} {
		if id := pod.Labels[label]; id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func newAllocationObserver(ctx context.Context, sb *SlurmBridge, cfg *slurmclient.Config) (*allocationObserver, error) {
	// Share this informer with scheduling, while keeping its refreshes separate
	// from the client used for writes and explicit live validation.
	reader, err := slurmclient.NewClient(cfg, &slurmclient.ClientOptions{CacheSyncPeriod: time.Second})
	if err != nil {
		return nil, err
	}
	pods := sb.handle.SharedInformerFactory().Core().V1().Pods()
	indexer := pods.Informer().GetIndexer()
	if _, exists := indexer.GetIndexers()[allocationJobIndex]; !exists {
		if err := pods.Informer().AddIndexers(cache.Indexers{allocationJobIndex: allocationJobIDs}); err != nil {
			return nil, fmt.Errorf("index allocation pods: %w", err)
		}
	}
	o := &allocationObserver{
		ctx: ctx, bridge: sb, pods: pods.Lister(), podIndex: indexer,
		hasSynced: pods.Informer().HasSynced,
		jobs:      reader.GetInformer(api.ObjectTypeV0044JobInfo),
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[client.ObjectKey](time.Second, 10*time.Second),
			workqueue.TypedRateLimitingQueueConfig[client.ObjectKey]{Name: "slurm-allocation"}),
	}
	o.jobs.SetEventHandler(slurmcache.ResourceEventHandlerFuncs{
		AddFunc:    o.jobChanged,
		UpdateFunc: func(_, newObj interface{}) { o.jobChanged(newObj) },
		DeleteFunc: o.jobChanged,
	})
	_, err = pods.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { o.enqueuePod(obj.(*corev1.Pod)) },
		UpdateFunc: func(oldObj, newObj interface{}) {
			o.podUpdated(oldObj.(*corev1.Pod), newObj.(*corev1.Pod))
		},
	})
	if err != nil {
		o.queue.ShutDown()
		return nil, fmt.Errorf("watch allocation readiness: %w", err)
	}
	go func() {
		<-ctx.Done()
		o.queue.ShutDown()
	}()
	return o, nil
}

func (o *allocationObserver) start() {
	o.once.Do(func() {
		go o.jobs.Run(o.ctx.Done())
		go func() {
			if !cache.WaitForCacheSync(o.ctx.Done(), o.hasSynced, func() bool {
				if !o.jobs.HasStarted() {
					return false
				}
				synced, err := o.jobs.HasSynced()
				return synced && err == nil
			}) {
				return
			}
			for o.processNext() {
			}
		}()
	})
}

func (o *allocationObserver) processNext() bool {
	key, shutdown := o.queue.Get()
	if shutdown {
		return false
	}
	defer o.queue.Done(key)
	if err := o.reconcile(o.ctx, key); err != nil {
		klog.FromContext(o.ctx).Error(err, "update allocation hint", "pod", key)
		o.queue.AddRateLimited(key)
	} else {
		o.queue.Forget(key)
	}
	return true
}

// Handlers only enqueue keys. Reading the current caches in the worker handles
// reordered Slurm callbacks, Pod registration after allocation, and retries.
func (o *allocationObserver) jobChanged(obj interface{}) {
	job := obj.(*api.V0044JobInfo)
	pods, err := o.podIndex.ByIndex(allocationJobIndex, string(job.GetKey()))
	if err != nil {
		klog.FromContext(o.ctx).Error(err, "find allocation pods", "jobId", job.GetKey())
		return
	}
	for _, obj := range pods {
		o.enqueuePod(obj.(*corev1.Pod))
	}
}

func (o *allocationObserver) enqueuePod(pod *corev1.Pod) {
	if o.pending(pod) && (pod.Labels[wellknown.LabelExternalJobId] != "" || pod.Labels[wellknown.LabelAllocatedJobId] != "") {
		o.queue.Add(client.ObjectKeyFromObject(pod))
	}
}

func (o *allocationObserver) pending(pod *corev1.Pod) bool {
	return pod.Spec.SchedulerName == o.bridge.schedulerName && pod.Spec.NodeName == "" &&
		pod.DeletionTimestamp == nil && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

func (o *allocationObserver) podUpdated(oldPod, newPod *corev1.Pod) {
	if !o.pending(newPod) {
		return
	}
	o.enqueuePod(newPod)
	if id := allocatedJobID(newPod); id != "" && id != allocatedJobID(oldPod) {
		// Pod updates repair active-queue order; Activate additionally bypasses
		// backoff for this transition. Never repeatedly activate an unchanged
		// allocation that a Kubernetes feasibility check might have rejected.
		o.bridge.activatePod(klog.FromContext(o.ctx), newPod)
	}
}

func (o *allocationObserver) reconcile(ctx context.Context, key client.ObjectKey) error {
	pod, err := o.pods.Pods(key.Namespace).Get(key.Name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !o.pending(pod) {
		return nil
	}
	want := ""
	if id := pod.Labels[wellknown.LabelExternalJobId]; id != "" {
		job := &api.V0044JobInfo{}
		err := o.jobs.Get(ctx, slurmclient.ObjectKey(id), job)
		if err != nil && !errors.Is(err, slurmerrors.ErrNotFound) {
			// A failed read does not establish allocation loss.
			return err
		}
		if err == nil && allocationMatchesPod(job, pod) {
			want = id
		}
	}
	if pod.Labels[wellknown.LabelAllocatedJobId] == want {
		return nil
	}
	updated := pod.DeepCopy()
	if want == "" {
		delete(updated.Labels, wellknown.LabelAllocatedJobId)
	} else {
		if updated.Labels == nil {
			updated.Labels = make(map[string]string)
		}
		updated.Labels[wellknown.LabelAllocatedJobId] = want
	}
	// ResourceVersion guards job replacement, binding and Pod recreation. Retry
	// conflicts and transient errors even if Slurm emits no further change.
	if err := o.bridge.Patch(ctx, updated, client.MergeFromWithOptions(pod, client.MergeFromWithOptimisticLock{})); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func allocationMatchesPod(job *api.V0044JobInfo, pod *corev1.Pod) bool {
	states := slurmutils.GetStateAsSet(job.JobState)
	if ptr.Deref(job.JobId, 0) <= 0 || ptr.Deref(job.Nodes, "") == "" ||
		!states.Has(api.V0044JobInfoJobStateRUNNING) || states.Has(api.V0044JobInfoJobStateCOMPLETING) {
		return false
	}
	var info externaljobinfo.ExternalJobInfo
	return externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, &info) == nil &&
		(slices.Contains(info.Pods, externaljobinfo.PodKey(pod)) ||
			slices.Contains(info.Pods, client.ObjectKeyFromObject(pod).String()))
}
