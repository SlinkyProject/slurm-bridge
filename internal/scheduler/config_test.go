// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/klog/v2"
	serverconfig "k8s.io/kubernetes/cmd/kube-scheduler/app/config"
	"k8s.io/kubernetes/pkg/features"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func schedulerConfigFile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scheduler.yaml")
	require.NoError(t, os.WriteFile(path, []byte("apiVersion: kubescheduler.config.k8s.io/v1\nkind: KubeSchedulerConfiguration\n"+data), 0o600))
	return path
}

func TestSchedulerConfigurationRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ config, wantErr string }{
		{"unknownSetting: true\n", "unknown field"},
		{"parallelism: 0\n", "parallelism"},
	} {
		t.Run(tc.wantErr, func(t *testing.T) {
			_, err := NewRunnable(schedulerConfigFile(t, tc.config), "")
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestSchedulerRejectsCoordinatedLeaderElection(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.CoordinatedLeaderElection, true)
	_, err := NewRunnable(schedulerConfigFile(t, ""), "")
	require.ErrorContains(t, err, "CoordinatedLeaderElection is not supported")
}

func TestKubeConfigOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://explicit.invalid
    insecure-skip-tls-verify: true
users:
- name: test
  user:
    token: test-token
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
`), 0o600))
	runnable, err := NewRunnable(schedulerConfigFile(t, "clientConnection:\n  kubeconfig: /missing/kubeconfig\n  qps: 42\n  burst: 84\n"), "")
	require.NoError(t, err)
	_, err = runnable.KubeConfig("")
	require.Error(t, err, "invalid scheduler connection must fail before starting either component")
	managerConfig, err := runnable.KubeConfig(path)
	require.NoError(t, err)
	require.Equal(t, "https://explicit.invalid", managerConfig.Host)
	require.Equal(t, "test-token", managerConfig.BearerToken)
	require.True(t, managerConfig.Insecure)
	require.Equal(t, float32(42), managerConfig.QPS)
	require.Equal(t, 84, managerConfig.Burst)
	// The manager's JSON wire format must not replace the scheduler's configured format.
	managerConfig.ContentType = "application/json"
	schedulerConfig := &serverconfig.Config{}
	require.NoError(t, runnable.opts.ApplyTo(klog.Background(), schedulerConfig))
	require.Equal(t, managerConfig.Host, schedulerConfig.KubeConfig.Host)
	require.Equal(t, managerConfig.BearerToken, schedulerConfig.KubeConfig.BearerToken)
	require.Equal(t, managerConfig.TLSClientConfig, schedulerConfig.KubeConfig.TLSClientConfig)
	require.Equal(t, "application/vnd.kubernetes.protobuf", schedulerConfig.KubeConfig.ContentType)
}

func TestLeaderElectionNamespaceOutsideCluster(t *testing.T) {
	for _, tc := range []struct {
		name, config, override, want string
	}{
		{name: "default", want: "kube-system"},
		{name: "scheduler configuration", config: "leaderElection:\n  resourceNamespace: slurm\n", want: "slurm"},
		{name: "explicit override", config: "leaderElection:\n  resourceNamespace: slurm\n", override: "custom", want: "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runnable, err := NewRunnable(schedulerConfigFile(t, tc.config), "")
			require.NoError(t, err)
			opts := ctrl.Options{
				LeaderElection:          true,
				LeaderElectionID:        "slurm-bridge-scheduler",
				LeaderElectionNamespace: tc.override,
				Metrics:                 metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress:  "0",
			}
			runnable.ConfigureManager(&opts)
			require.Equal(t, tc.want, opts.LeaderElectionNamespace)
			// Manager construction must succeed without the pod namespace file.
			_, err = ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, opts)
			require.NoError(t, err)
		})
	}
}
