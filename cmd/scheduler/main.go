// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

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
	"k8s.io/component-base/metrics/legacyregistry"
	_ "k8s.io/component-base/metrics/prometheus/clientgo"
	_ "k8s.io/component-base/metrics/prometheus/version"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	lws "sigs.k8s.io/lws/api/leaderworkerset/v1"
	_ "sigs.k8s.io/scheduler-plugins/apis/config/scheme"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmtoken "github.com/SlinkyProject/slurm-client/pkg/client/token"

	"github.com/SlinkyProject/slurm-bridge/internal/config"
	"github.com/SlinkyProject/slurm-bridge/internal/controller/node"
	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/controller/registration"
	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmnode"
	bridgescheduler "github.com/SlinkyProject/slurm-bridge/internal/scheduler"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge"
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
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

type Flags struct {
	enableScheduler         bool
	enableLeaderElection    bool
	leaderElectionNamespace string
	configFile              string
	metricsAddr             string
	probeAddr               string
	secureMetrics           bool
	enableHTTP2             bool
	schedulerConfig         string
	schedulerFeatureGates   string
	controllers             string
}

func bindFlags(flags *Flags, fs *flag.FlagSet) {
	fs.BoolVar(&flags.enableScheduler, "enable-scheduler", true, "Run the scheduler alongside the selected controllers.")
	fs.StringVar(&flags.controllers, "controllers", "node,registration",
		"Comma-separated controllers to enable: node,registration. Empty runs only the scheduler.")
	fs.StringVar(&flags.schedulerConfig, "scheduler-config", "/etc/kubernetes/scheduler-config.yaml",
		"The path to the KubeSchedulerConfiguration file.")
	fs.StringVar(&flags.schedulerFeatureGates, "scheduler-feature-gates", "",
		"Feature gates for the embedded scheduler; applied to the entire process.")
	fs.StringVar(&flags.configFile, "config", config.ConfigFile,
		"The path to the configuration file.")

	fs.StringVar(&flags.metricsAddr, "metrics-bind-address", ":8080",
		"The address the metric endpoint binds to.")
	fs.StringVar(&flags.probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.",
	)
	fs.BoolVar(&flags.enableLeaderElection, "leader-elect", true,
		("Enable leader election for the scheduler. " +
			"Only the elected replica schedules pods and runs controllers."),
	)
	fs.StringVar(&flags.leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace for the leader-election lease. Defaults to the scheduler configuration, or the pod namespace in controllers-only mode.")
	fs.BoolVar(&flags.secureMetrics, "metrics-secure", true,
		"Serve metrics over HTTPS. Authentication and authorization are always required.")
	fs.BoolVar(&flags.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
}

func main() {
	var flags Flags
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	bindFlags(&flags, flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	klog.SetLoggerWithOptions(ctrl.Log.WithName("scheduler"), klog.ContextualLogger(true))
	ctx := ctrl.SetupSignalHandler()
	enabled, err := parseControllers(flags.controllers)
	if err != nil {
		setupLog.Error(err, "invalid controller selection")
		os.Exit(1)
	}
	slurmbridge.ConfigFile = flags.configFile
	schedulerRunnable, err := newSchedulerRunnable(flags)
	if err != nil {
		setupLog.Error(err, "configure scheduler")
		os.Exit(1)
	}

	// Disable HTTP/2 by default to mitigate stream-reset attacks.
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !flags.enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
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

	kubeConfig, err := kubernetesConfig(schedulerRunnable)
	if err != nil {
		setupLog.Error(err, "configure Kubernetes connection")
		os.Exit(1)
	}
	// The common Workload API adapter uses JSON across supported API versions.
	kubeConfig.ContentType = runtime.ContentTypeJSON
	kubeConfig.AcceptContentTypes = runtime.ContentTypeJSON
	var workloadAPI *slurmjobir.WorkloadAPI
	if enabled["registration"] {
		workloadAPI, err = slurmjobir.NewWorkloadAPI(kubeConfig, scheme)
		if err != nil {
			setupLog.Error(err, "register Workload API")
			os.Exit(1)
		}
	}
	managerOpts := ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:    flags.metricsAddr,
			SecureServing:  flags.secureMetrics,
			TLSOpts:        tlsOpts,
			FilterProvider: filters.WithAuthenticationAndAuthorization,
			ExtraHandlers:  map[string]http.Handler{"/metrics/scheduler": legacyregistry.Handler()},
		},
		HealthProbeBindAddress:  flags.probeAddr,
		LeaderElection:          flags.enableLeaderElection,
		LeaderElectionID:        cfg.SchedulerName,
		LeaderElectionNamespace: flags.leaderElectionNamespace,
	}
	if schedulerRunnable != nil {
		schedulerRunnable.ConfigureManager(&managerOpts)
	}
	mgr, err := ctrl.NewManager(kubeConfig, managerOpts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if len(enabled) > 0 {
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
		go slurmClient.Start(ctx)

		if enabled["node"] {
			if err := nodeutils.SetupFieldIndexers(mgr); err != nil {
				setupLog.Error(err, "unable to set up node field indexers")
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
		}
		if enabled["registration"] {
			registrationController := registration.NewReconciler(mgr.GetClient(), slurmClient, cfg.SchedulerName, draRegistry, workloadAPI, cfg.MCSLabel, cfg.Partition)
			if err := registrationController.SetupWithManager(mgr); err != nil {
				setupLog.Error(err, "unable to create controller", "controller", "Registration")
				os.Exit(1)
			}
		}
	}
	// +kubebuilder:scaffold:builder
	if schedulerRunnable != nil {
		if err := schedulerRunnable.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "add embedded scheduler")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func kubernetesConfig(scheduler *bridgescheduler.Runnable) (*rest.Config, error) {
	if scheduler != nil {
		return scheduler.KubeConfig(flag.Lookup("kubeconfig").Value.String())
	}
	return ctrl.GetConfig()
}

func parseControllers(value string) (map[string]bool, error) {
	enabled := make(map[string]bool)
	if strings.TrimSpace(value) == "" {
		return enabled, nil
	}
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "node", "registration":
			enabled[name] = true
		default:
			return nil, fmt.Errorf("unknown controller %q: supported controllers are node,registration", name)
		}
	}
	return enabled, nil
}

// Apply feature gates before constructing the manager's Workload API adapter,
// including when the process runs controllers without the scheduler.
func newSchedulerRunnable(flags Flags) (*bridgescheduler.Runnable, error) {
	if !flags.enableScheduler {
		return nil, utilfeature.DefaultMutableFeatureGate.Set(flags.schedulerFeatureGates)
	}
	return bridgescheduler.NewRunnable(flags.schedulerConfig, flags.schedulerFeatureGates,
		app.WithPlugin(slurmbridge.Name, slurmbridge.New))
}
