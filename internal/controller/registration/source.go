// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmcache "github.com/SlinkyProject/slurm-client/pkg/cache"

	"github.com/SlinkyProject/slurm-bridge/internal/config"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
)

type jobSource struct{ reconciler *Reconciler }

func (s *jobSource) Start(ctx context.Context, queue workqueue.TypedRateLimitingInterface[Request]) error {
	r := s.reconciler
	r.enqueue = queue.Add
	enqueue := func(obj any) { enqueueJob(obj, queue.Add) }
	// Callbacks only decode event data and enqueue; reconciliation reads the cache.
	r.informer.SetEventHandler(slurmcache.ResourceEventHandlerFuncs{
		AddFunc: enqueue, UpdateFunc: func(old, current any) { enqueue(old); enqueue(current) }, DeleteFunc: enqueue,
	})
	go func() {
		defer r.informer.UnsetEventHandler()
		ticker := time.NewTicker(config.SlurmCacheSyncPeriod)
		defer ticker.Stop()
		for {
			// Reconstruct work from the cache on startup and periodically. This also
			// recovers orphaned duplicates and events preceding handler installation.
			jobs := &api.V0044JobInfoObjectList{}
			if err := r.informer.List(ctx, jobs); err != nil {
				if ctx.Err() == nil {
					log.FromContext(ctx).Error(err, "Rebuild allocation registration queue")
				}
			} else {
				for i := range jobs.Items {
					enqueue(&jobs.Items[i])
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func enqueueJob(obj any, enqueue func(Request)) {
	job, ok := obj.(*api.V0044JobInfo)
	if !ok {
		return
	}
	var info externaljobinfo.ExternalJobInfo
	if externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, &info) != nil {
		return
	}
	switch {
	case info.CompositePodGroup != nil:
		enqueue(groupKey(*info.CompositePodGroup))
	case info.PodGroup != nil:
		enqueue(groupKey(*info.PodGroup))
	default:
		for _, key := range info.Pods {
			ref := externaljobinfo.ParsePodKey(key)
			enqueue(Request{Kind: "Pod", Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID})
		}
	}
}
