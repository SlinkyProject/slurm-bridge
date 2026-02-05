// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmjob/slurmcontrol"
)

const (
	ControllerName = "slurmjob-runnable"
)

// SlurmJobRunnable reconciles a Pod object
type SlurmJobRunnable struct {
	client.Client

	eventCh chan<- event.GenericEvent

	slurmControl slurmcontrol.SlurmControlInterface
}

// SetupWithManager sets up the controller with the Manager.
func (r *SlurmJobRunnable) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(r)
}

var _ manager.Runnable = (*SlurmJobRunnable)(nil)

func (r *SlurmJobRunnable) Start(ctx context.Context) error {
	logger := log.FromContext(ctx)

	interval := 30 * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error(err, "SlurmJob runnable encountered an error")
			}
		}
	}
}

func NewRunnable(kubeClient client.Client, slurmClient slurmclient.Client, eventCh chan<- event.GenericEvent) *SlurmJobRunnable {
	r := &SlurmJobRunnable{
		Client:       kubeClient,
		eventCh:      eventCh,
		slurmControl: slurmcontrol.NewControl(slurmClient),
	}
	return r
}
