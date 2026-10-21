// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"flag"
	"net/http"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	lws "sigs.k8s.io/lws/api/leaderworkerset/v1"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmtoken "github.com/SlinkyProject/slurm-client/pkg/client/token"

	"github.com/SlinkyProject/slurm-bridge/internal/config"
	"github.com/SlinkyProject/slurm-bridge/internal/controller/node"
	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/controller/pod"
	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmnode"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	// Register only the APIs used here; the Workload API wire types are selected
	// after discovery and must not collide with client-go's built-in types.
	utilruntime.Must(metav1.AddMetaToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	utilruntime.Must(coordinationv1.AddToScheme(scheme))
	utilruntime.Must(resourcev1.AddToScheme(scheme))
	utilruntime.Must(jobset.AddToScheme(scheme))
	utilruntime.Must(lws.AddToScheme(scheme))
	utilruntime.Must(sched.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create;get;list;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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
	flag.Func("feature-gates", "A set of key=value pairs that enable or disable feature gates.",
		utilfeature.DefaultMutableFeatureGate.Set)
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

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets;deployments;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=scheduling.x-k8s.io,resources=podgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=podgroups;workloads;compositepodgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=deviceclasses,verbs=get;list;watch

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

	kubeConfig := rest.CopyConfig(ctrl.GetConfigOrDie())
	// The common Workload API adapter uses JSON across supported API versions.
	kubeConfig.ContentType = runtime.ContentTypeJSON
	kubeConfig.AcceptContentTypes = runtime.ContentTypeJSON
	_, err := slurmjobir.NewWorkloadAPI(kubeConfig, scheme)
	if err != nil {
		setupLog.Error(err, "register Workload API")
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
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

	if err := nodeutils.SetupFieldIndexers(mgr); err != nil {
		setupLog.Error(err, "unable to set up field indexers")
		os.Exit(1)
	}

	data, err := os.ReadFile(flags.configFile)
	if err != nil {
		setupLog.Error(err, "unable to read config file", "file", flags.configFile)
		os.Exit(1)
	}
	cfg, err := config.Unmarshal(data)
	if err != nil {
		setupLog.Error(err, "unable to parse config file", "file", flags.configFile)
		os.Exit(1)
	}
	draRegistry, err := cfg.DRARegistry()
	if err != nil {
		setupLog.Error(err, "unable to configure DRA device profiles")
		os.Exit(1)
	}

	clientConfig := &slurmclient.Config{
		Server:        cfg.SlurmRestApi,
		TokenProvider: slurmtoken.FileProvider{Path: os.Getenv("SLURM_JWT_FILE")},
		HTTPClient:    &http.Client{Timeout: config.SlurmClientTimeout},
	}
	slurmClient, err := slurmclient.NewClient(clientConfig, &slurmclient.ClientOptions{CacheSyncPeriod: config.SlurmCacheSyncPeriod})
	if err != nil {
		setupLog.Error(err, "unable to create slurm client")
		os.Exit(1)
	}

	nodeEventCh := make(chan event.GenericEvent, 100)
	if err := node.NewReconciler(mgr.GetClient(), slurmClient, cfg.SchedulerName, nodeEventCh, draRegistry).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Node")
		os.Exit(1)
	}
	if err := slurmnode.NewRunnable(mgr.GetClient(), slurmClient, nodeEventCh).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create runnable", "runnable", "SlurmNode")
		os.Exit(1)
	}
	podEventCh := make(chan event.GenericEvent, 100)
	podController := pod.NewReconciler(mgr.GetClient(), slurmClient, cfg.SchedulerName, podEventCh)
	if err := podController.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Pod")
		os.Exit(1)
	}
	jobRunnable := slurmjob.NewRunnable(mgr.GetClient(), mgr.GetAPIReader(), slurmClient, podEventCh)
	if err := jobRunnable.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create runnable", "runnable", "SlurmJob")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	go slurmClient.Start(ctx)
	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
