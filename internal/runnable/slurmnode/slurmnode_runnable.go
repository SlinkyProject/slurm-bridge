// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmnode

import (
	"context"
	"errors"
	"time"

	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	slurmapi "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmnode/slurmcontrol"
)

const (
	ControllerName = "slurmnode-runnable"
)

// SlurmNodeRunnable reconciles a Pod object
type SlurmJobRunnable struct {
	client.Client

	eventCh chan<- event.GenericEvent

	slurmControl slurmcontrol.SlurmControlInterface
	informer     slurmclient.InformerCache
}

// SetupWithManager sets up the controller with the Manager.
func (r *SlurmJobRunnable) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(r)
}

var _ manager.Runnable = (*SlurmJobRunnable)(nil)

func (r *SlurmJobRunnable) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)

	if !cache.WaitForCacheSync(ctx.Done(), r.informer.HasStarted) {
		return nil
	}

	interval := 30 * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := r.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error(err, "SlurmNode runnable encountered an error")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func NewRunnable(kubeClient client.Client, slurmClient slurmclient.Client, eventCh chan<- event.GenericEvent) *SlurmJobRunnable {
	r := &SlurmJobRunnable{
		Client:       kubeClient,
		eventCh:      eventCh,
		slurmControl: slurmcontrol.NewControl(slurmClient),
		informer:     slurmClient.GetInformer(slurmapi.ObjectTypeV0044Node),
	}
	return r
}
