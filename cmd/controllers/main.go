// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/config"
	"github.com/SlinkyProject/slurm-bridge/internal/controller/node"
	"github.com/SlinkyProject/slurm-bridge/internal/controller/pod"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	//+kubebuilder:scaffold:scheme
}

// Input flags to the command
type Flags struct {
	enableLeaderElection bool
	configFile           string
	metricsAddr          string
	probeAddr            string
	secureMetrics        bool
	enableHTTP2          bool
}

func parseFlags(flags *Flags) {
	flag.StringVar(&flags.configFile, "config", config.ConfigFile,
		"The path to the configuration file.")

	flag.StringVar(&flags.metricsAddr, "metrics-bind-address", ":8080",
		"The address the metric endpoint binds to.")
	flag.StringVar(&flags.probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.",
	)
	flag.BoolVar(&flags.enableLeaderElection, "leader-elect", false,
		("Enable leader election for controller manager. " +
			"Enabling this will ensure there is only one active controller manager."),
	)
	flag.BoolVar(&flags.secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&flags.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.Parse()
}

func main() {
	var flags Flags
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	parseFlags(&flags)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !flags.enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   flags.metricsAddr,
			SecureServing: flags.secureMetrics,
			TLSOpts:       tlsOpts,
		},
		HealthProbeBindAddress:        flags.probeAddr,
		LeaderElection:                flags.enableLeaderElection,
		LeaderElectionID:              "69d5fe47.my.slinky.slurm.net",
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	data, err := os.ReadFile(flags.configFile)
	if err != nil {
		setupLog.Error(err, "unable to read config file", "file", flags.configFile)
		os.Exit(1)
	}
	cfg := config.UnmarshalOrDie(data)

	clientConfig := &slurmclient.Config{
		Server: cfg.SlurmRestApi,
		AuthToken: func() string {
			token, _ := os.LookupEnv("SLURM_JWT")
			return token
		}(),
	}
	slurmClient, err := slurmclient.NewClient(clientConfig)
	if err != nil {
		setupLog.Error(err, "unable to create slurm client")
		os.Exit(1)
	}
	go slurmClient.Start(context.Background())

	if err = (&node.NodeReconciler{
		Client:        mgr.GetClient(),
		SchedulerName: cfg.SchedulerName,
		Scheme:        mgr.GetScheme(),
		SlurmClient:   slurmClient,
		EventCh:       make(chan event.GenericEvent, 100),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Node")
		os.Exit(1)
	}
	if err = (&pod.PodReconciler{
		Client:        mgr.GetClient(),
		SchedulerName: cfg.SchedulerName,
		Scheme:        mgr.GetScheme(),
		SlurmClient:   slurmClient,
		EventCh:       make(chan event.GenericEvent, 100),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Pod")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
