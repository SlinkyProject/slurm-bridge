// SPDX-FileCopyrightText: Copyright 2014 The Kubernetes Authors.
// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package scheduler integrates Kubernetes scheduling with controller-runtime.
package scheduler

import (
	"context"
	"fmt"
	"time"

	scheduling "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers/internalinterfaces"
	schedulinginformers "k8s.io/client-go/informers/scheduling/v1beta1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	serverconfig "k8s.io/kubernetes/cmd/kube-scheduler/app/config"
	"k8s.io/kubernetes/cmd/kube-scheduler/app/options"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/workloadclient"
)

// setup constructs the scheduler and its version-aware PodGroup client from validated options.
// It follows kube-scheduler v1.37.1 and must be checked when updating Kubernetes.
func setup(ctx context.Context, opts *options.Options, registryOptions ...app.Option) (*serverconfig.CompletedConfig, *scheduler.Scheduler, error) {
	c, err := opts.Config(ctx)
	if err != nil {
		return nil, nil, err
	}
	if utilfeature.DefaultFeatureGate.Enabled(features.GenericWorkload) {
		c.Client, err = workloadclient.New(c.KubeConfig, c.Client)
		if err != nil {
			return nil, nil, fmt.Errorf("configure scheduler PodGroup client: %w", err)
		}
		// Register the adapted PodGroup informer before scheduler construction.
		// Keep the existing factory and Pod informer, which already owns its
		// metrics identity and scheduler-specific filtering and transforms.
		c.InformerFactory.InformerFor(&scheduling.PodGroup{}, func(_ kubernetes.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
			return schedulinginformers.NewPodGroupInformerWithOptions(c.Client, metav1.NamespaceAll, internalinterfaces.InformerOptions{
				ResyncPeriod: resyncPeriod,
				Indexers:     cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
				InformerName: opts.InformerName,
			})
		})
	}
	cc := c.Complete()
	registry := make(runtime.Registry)
	for _, option := range registryOptions {
		if err := option(registry); err != nil {
			return nil, nil, err
		}
	}
	var completedProfiles []schedulerconfig.KubeSchedulerProfile
	sched, err := scheduler.New(ctx, cc.Client, cc.InformerFactory, cc.DynInformerFactory,
		func(name string) events.EventRecorderLogger { return cc.EventBroadcaster.NewRecorder(name) },
		scheduler.WithComponentConfigVersion(cc.ComponentConfig.APIVersion),
		scheduler.WithKubeConfig(cc.KubeConfig),
		scheduler.WithProfiles(cc.ComponentConfig.Profiles...),
		scheduler.WithPercentageOfNodesToScore(cc.ComponentConfig.PercentageOfNodesToScore),
		scheduler.WithFrameworkOutOfTreeRegistry(registry),
		scheduler.WithPodMaxBackoffSeconds(cc.ComponentConfig.PodMaxBackoffSeconds),
		scheduler.WithPodInitialBackoffSeconds(cc.ComponentConfig.PodInitialBackoffSeconds),
		scheduler.WithPodMaxInUnschedulablePodsDuration(cc.PodMaxInUnschedulablePodsDuration),
		scheduler.WithExtenders(cc.ComponentConfig.Extenders...),
		scheduler.WithParallelism(cc.ComponentConfig.Parallelism),
		scheduler.WithBuildFrameworkCapturer(func(profile schedulerconfig.KubeSchedulerProfile) {
			completedProfiles = append(completedProfiles, profile)
		}),
	)
	if err != nil {
		return nil, nil, err
	}
	if err := options.LogOrWriteConfig(klog.FromContext(ctx), opts.WriteConfigTo, &cc.ComponentConfig, completedProfiles); err != nil {
		return nil, nil, err
	}
	return &cc, sched, nil
}
