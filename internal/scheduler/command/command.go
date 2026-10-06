// SPDX-FileCopyrightText: Copyright 2014 The Kubernetes Authors.
// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package command wires the upstream scheduler with a version-aware PodGroup
// client before its shared informers and scheduling framework are constructed.
// Command setup follows k8s.io/kubernetes/cmd/kube-scheduler/app at v1.37.1;
// serving, leader election and the scheduler lifecycle remain in upstream Run.
package command

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	scheduling "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers/internalinterfaces"
	schedulinginformers "k8s.io/client-go/informers/scheduling/v1beta1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/events"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	basecompatibility "k8s.io/component-base/compatibility"
	"k8s.io/component-base/featuregate"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/term"
	"k8s.io/component-base/version/verflag"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	serverconfig "k8s.io/kubernetes/cmd/kube-scheduler/app/config"
	"k8s.io/kubernetes/cmd/kube-scheduler/app/options"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/latest"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/workloadclient"
)

// New constructs the scheduler command with native PodGroup API adaptation.
func New(registryOptions ...app.Option) *cobra.Command {
	opts := options.NewOptions()
	cmd := &cobra.Command{
		Use:  "slurm-bridge-scheduler",
		Long: "Run the Kubernetes scheduler with Slurm Bridge plugins.",
		Args: cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return opts.ComponentGlobalsRegistry.Set()
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			verflag.PrintAndExitIfRequested()
			fg := opts.ComponentGlobalsRegistry.FeatureGateFor(basecompatibility.DefaultKubeComponent)
			if err := logsapi.ValidateAndApply(opts.Logs, fg); err != nil {
				return err
			}
			cliflag.PrintFlags(cmd.Flags())
			if opts.InformerName == nil {
				informerName, err := cache.NewInformerName("kube-scheduler")
				if err != nil {
					return err
				}
				opts.InformerName = informerName
			}
			ctx, cancel := context.WithCancel(server.SetupSignalContext())
			defer cancel()
			cc, sched, err := setup(ctx, opts, registryOptions...)
			if err != nil {
				return err
			}
			fg.(featuregate.MutableFeatureGate).AddMetrics()
			opts.ComponentGlobalsRegistry.AddMetrics()
			return app.Run(ctx, cc, sched)
		},
	}
	nfs := opts.Flags
	verflag.AddFlags(nfs.FlagSet("global"))
	globalflag.AddGlobalFlags(nfs.FlagSet("global"), cmd.Name(), logs.SkipLoggingConfigurationFlags())
	for _, flags := range nfs.FlagSets {
		cmd.Flags().AddFlagSet(flags)
	}
	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cliflag.SetUsageAndHelpFunc(cmd, *nfs, cols)
	if err := cmd.MarkFlagFilename("config", "yaml", "yml", "json"); err != nil {
		klog.Background().Error(err, "Failed to mark flag filename")
	}
	return cmd
}

func setup(ctx context.Context, opts *options.Options, registryOptions ...app.Option) (*serverconfig.CompletedConfig, *scheduler.Scheduler, error) {
	cfg, err := latest.Default()
	if err != nil {
		return nil, nil, err
	}
	opts.ComponentConfig = cfg
	if errs := opts.Validate(); len(errs) != 0 {
		return nil, nil, utilerrors.NewAggregate(errs)
	}
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
