// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	kubefeatures "k8s.io/kubernetes/pkg/features"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestParseControllers(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  map[string]bool
	}{
		{"node,registration", map[string]bool{"node": true, "registration": true}},
		{"node", map[string]bool{"node": true}},
		{"registration", map[string]bool{"registration": true}},
		{"", map[string]bool{}},
		{" registration, node, registration ", map[string]bool{"node": true, "registration": true}},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := parseControllers(tc.value)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for _, value := range []string{"nodes", "scheduler", "registration,unknown", "registration,"} {
		_, err := parseControllers(value)
		require.ErrorContains(t, err, "unknown controller")
	}
}

func TestSchedulerFeatureGates(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, true)
	path := filepath.Join(t.TempDir(), "scheduler.yaml")
	require.NoError(t, os.WriteFile(path, []byte("apiVersion: kubescheduler.config.k8s.io/v1\nkind: KubeSchedulerConfiguration\n"), 0o600))
	_, err := newSchedulerRunnable(Flags{enableScheduler: true, schedulerConfig: path, schedulerFeatureGates: "GenericWorkload=false"})
	require.NoError(t, err)
	require.False(t, utilfeature.DefaultFeatureGate.Enabled(features.GenericWorkload))
	require.False(t, utilfeature.DefaultFeatureGate.Enabled(kubefeatures.GenericWorkload),
		"the single Workload gate must disable native scheduling and registration")
}

func TestControllerSchemeSupportsWorkloadAdapter(t *testing.T) {
	_, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1)
	require.NoError(t, err)
}

func TestControllerOnlyWorkloadFeatureGate(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, true)
	runnable, err := newSchedulerRunnable(Flags{schedulerFeatureGates: "GenericWorkload=false"})
	require.NoError(t, err)
	require.Nil(t, runnable)
	require.False(t, utilfeature.DefaultFeatureGate.Enabled(features.GenericWorkload))
	api, err := slurmjobir.NewWorkloadAPI(nil, nil)
	require.NoError(t, err)
	require.Nil(t, api, "disabled Workload support must skip discovery for controllers too")
}

func TestDefaultControllersIncludeRegistration(t *testing.T) {
	fs := flag.NewFlagSet("scheduler", flag.ContinueOnError)
	var flags Flags
	bindFlags(&flags, fs)
	require.NoError(t, fs.Parse(nil))
	enabled, err := parseControllers(flags.controllers)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"node": true, "registration": true}, enabled)
}

func TestKubernetesConfig(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, true)
	writeConfig := func(name string) string {
		path := filepath.Join(t.TempDir(), "kubeconfig")
		data := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://%s.invalid
contexts:
- name: test
  context:
    cluster: test
current-context: test
`, name)
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		return path
	}
	environmentConfig := writeConfig("environment")
	schedulerConfig := writeConfig("scheduler")
	explicitConfig := writeConfig("explicit")
	t.Setenv("KUBECONFIG", environmentConfig)
	previous := flag.Lookup("kubeconfig").Value.String()
	t.Cleanup(func() { require.NoError(t, flag.Set("kubeconfig", previous)) })
	for _, tc := range []struct {
		name, override, wantHost string
		scheduler                bool
	}{
		{name: "scheduler config overrides environment", scheduler: true, wantHost: "https://scheduler.invalid"},
		{name: "explicit flag overrides scheduler config", scheduler: true, override: explicitConfig, wantHost: "https://explicit.invalid"},
		{name: "controllers only environment", wantHost: "https://environment.invalid"},
		{name: "controllers only explicit flag", override: explicitConfig, wantHost: "https://explicit.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, flag.Set("kubeconfig", tc.override))
			path := filepath.Join(t.TempDir(), "scheduler.yaml")
			require.NoError(t, os.WriteFile(path, []byte("apiVersion: kubescheduler.config.k8s.io/v1\nkind: KubeSchedulerConfiguration\nclientConnection:\n  kubeconfig: "+schedulerConfig+"\n"), 0o600))
			runnable, err := newSchedulerRunnable(Flags{enableScheduler: tc.scheduler, schedulerConfig: path})
			require.NoError(t, err)
			cfg, err := kubernetesConfig(runnable)
			require.NoError(t, err)
			require.Equal(t, tc.wantHost, cfg.Host)
		})
	}
}
