// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/server/mux"
	"k8s.io/apiserver/pkg/server/routes"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	serverconfig "k8s.io/kubernetes/cmd/kube-scheduler/app/config"
	"k8s.io/kubernetes/cmd/kube-scheduler/app/options"
	"k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Runnable runs scheduling while its manager holds leadership.
type Runnable struct {
	opts            *options.Options
	delayCache      bool
	registryOptions []app.Option
	elected         <-chan struct{}
	initialized     atomic.Bool
	ready           atomic.Bool
}

var _ manager.Runnable = (*Runnable)(nil)
var _ manager.LeaderElectionRunnable = (*Runnable)(nil)

// NewRunnable loads the scheduler configuration and applies process-wide feature
// gates. Call it before constructing the manager, then call ConfigureManager.
func NewRunnable(configFile, featureGates string, registryOptions ...app.Option) (*Runnable, error) {
	if configFile == "" {
		return nil, fmt.Errorf("scheduler configuration file is required")
	}
	opts := options.NewOptions()
	opts.SecureServing.BindPort = 0
	if err := opts.Flags.FlagSet("feature gate").Set("feature-gates", featureGates); err != nil {
		return nil, err
	}
	if err := opts.ComponentGlobalsRegistry.Set(); err != nil {
		return nil, err
	}
	if utilfeature.DefaultFeatureGate.Enabled(features.CoordinatedLeaderElection) {
		return nil, fmt.Errorf("CoordinatedLeaderElection is not supported by the embedded scheduler")
	}
	cfg, err := options.LoadConfigFromFile(klog.Background(), configFile)
	if err != nil {
		return nil, fmt.Errorf("load scheduler configuration: %w", err)
	}
	// The manager owns leader election for the entire process.
	cfg.LeaderElection.LeaderElect = false
	opts.ComponentConfig = cfg
	if err := utilerrors.NewAggregate(opts.Validate()); err != nil {
		return nil, err
	}
	return &Runnable{opts: opts, delayCache: cfg.DelayCacheUntilActive, registryOptions: registryOptions}, nil
}

// KubeConfig resolves the connection used by both the manager and scheduler.
// An explicit --kubeconfig overrides clientConnection.kubeconfig for both.
// Call before constructing the manager or starting the scheduler.
func (r *Runnable) KubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		r.opts.ComponentConfig.ClientConnection.Kubeconfig = kubeconfig
	}
	c := &serverconfig.Config{}
	if err := r.opts.ApplyTo(klog.Background(), c); err != nil {
		return nil, err
	}
	return c.KubeConfig, nil
}

// ConfigureManager applies scheduler lease, shutdown, and profiling settings before
// constructing the manager.
func (r *Runnable) ConfigureManager(opts *manager.Options) {
	if opts.LeaderElectionNamespace == "" {
		opts.LeaderElectionNamespace = r.opts.ComponentConfig.LeaderElection.ResourceNamespace
	}
	// Scheduler.Run does not join every binding goroutine before returning.
	opts.LeaderElectionReleaseOnCancel = false
	if r.opts.ComponentConfig.EnableProfiling {
		if opts.Metrics.ExtraHandlers == nil {
			opts.Metrics.ExtraHandlers = make(map[string]http.Handler)
		}
		handler := mux.NewPathRecorderMux("scheduler")
		routes.Profiling{}.Install(handler)
		opts.Metrics.ExtraHandlers["/debug/pprof/"] = handler
	}
}

// SetupWithManager registers the scheduler and its readiness check.
func (r *Runnable) SetupWithManager(mgr manager.Manager) error {
	r.elected = mgr.Elected()
	if err := mgr.Add(r); err != nil {
		return err
	}
	return mgr.AddReadyzCheck("scheduler", r.Check)
}

// NeedLeaderElection is false so standby replicas can initialize before election.
func (*Runnable) NeedLeaderElection() bool { return false }

// Check requires initialized plugins and synchronized caches, except when a
// standby defers cache initialization with DelayCacheUntilActive.
func (r *Runnable) Check(*http.Request) error {
	if !r.initialized.Load() {
		return fmt.Errorf("scheduler is not initialized")
	}
	select {
	case <-r.elected:
	default:
		if r.delayCache {
			return nil
		}
	}
	if !r.ready.Load() {
		return fmt.Errorf("scheduler caches and handlers are not synchronized")
	}
	return nil
}

// Start runs until cancellation or an error. SetupWithManager must be called first.
func (r *Runnable) Start(ctx context.Context) error {
	if r.elected == nil {
		return fmt.Errorf("scheduler must be registered with SetupWithManager")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if r.opts.ComponentConfig.EnableProfiling && r.opts.ComponentConfig.EnableContentionProfiling {
		runtime.SetBlockProfileRate(1)
	}
	name, err := cache.NewInformerName("slurm-bridge-scheduler")
	if err != nil {
		return err
	}
	defer name.Release()
	r.opts.InformerName = name
	cc, sched, err := setup(ctx, r.opts, r.registryOptions...)
	if err != nil {
		return fmt.Errorf("set up embedded scheduler: %w", err)
	}
	cc.EventBroadcaster.StartRecordingToSink(ctx.Done())
	defer cc.EventBroadcaster.Shutdown()
	// Shutdown waits for informer goroutines before releasing their metrics name.
	defer cc.InformerFactory.Shutdown()
	if cc.DynInformerFactory != nil {
		defer cc.DynInformerFactory.Shutdown()
	}
	started := false
	defer func() {
		if !started {
			// Close resources if Scheduler.Run has not taken ownership.
			if sched.APIDispatcher != nil {
				sched.APIDispatcher.Close()
			}
			sched.SchedulingQueue.Close()
			_ = sched.Profiles.Close()
		}
	}()
	defer cancel()
	r.initialized.Store(true)
	defer r.initialized.Store(false)
	if r.delayCache {
		select {
		case <-ctx.Done():
			return nil
		case <-r.elected:
		}
	}

	cc.InformerFactory.Start(ctx.Done())
	if cc.DynInformerFactory != nil {
		cc.DynInformerFactory.Start(ctx.Done())
	}
	cc.InformerFactory.WaitForCacheSync(ctx.Done())
	if cc.DynInformerFactory != nil {
		cc.DynInformerFactory.WaitForCacheSync(ctx.Done())
	}
	if err := sched.WaitForHandlersSync(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("synchronize scheduler handlers: %w", err)
	}
	if ctx.Err() != nil {
		return nil
	}
	r.ready.Store(true)
	defer r.ready.Store(false)
	select {
	case <-ctx.Done():
		return nil
	case <-r.elected:
	}
	started = true
	sched.Run(ctx)
	return nil
}
