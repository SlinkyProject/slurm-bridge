// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/test"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/types"
	"sigs.k8s.io/e2e-framework/support/kind"
)

// TestMain configures the environment within which all e2e-tests are run
func TestMain(m *testing.M) {
	test.Testenv = env.New()
	kindClusterName := envconf.RandomName("test-e2e", 16)
	test.Basepath = test.GetBasePath()

	test.TestUID = envconf.RandomName("testing", 16)
	bridgeName := "ghcr.io/slinkyproject/slurm-bridge:" + "e2e"
	controllersName := "ghcr.io/slinkyproject/slurm-controllers:" + "e2e"
	admissionName := "ghcr.io/slinkyproject/slurm-admission:" + "e2e"
	err := test.BuildBridgeImages(bridgeName, controllersName, admissionName)
	if err != nil {
		fmt.Printf("Failed to build images for slurm-bridge: %v", err)
		os.Exit(1)
	}

	// Use pre-defined environment funcs to create a kind cluster prior to test run
	test.Testenv.Setup(
		envfuncs.CreateClusterWithConfig(kind.NewProvider(), kindClusterName, test.Basepath+"hack/kind.yaml"),
		envfuncs.LoadDockerImageToCluster(kindClusterName, bridgeName),
		envfuncs.LoadDockerImageToCluster(kindClusterName, controllersName),
		envfuncs.LoadDockerImageToCluster(kindClusterName, admissionName),
		envfuncs.CreateNamespace("slinky"),
		envfuncs.CreateNamespace("slurm"),
		envfuncs.CreateNamespace("slurm-bridge"),
		envfuncs.CreateNamespace("cert-manager"),
		// prerequisites for slurm-bridge
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.CertMgrInstall(config)
		},
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.SlurmOperatorCRDInstall(config)
		},
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.SlurmOperatorInstall(config)
		},
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.SlurmOperatorWebhookWaitReady(config)
		},
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.SlurmChartInstall(config)
		},
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.LabelWorkerNodesAsExternal(ctx, config)
		},
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.AnnotateExternalNodePartitions(ctx, config)
		},
	)

	// Use pre-defined environment funcs to teardown kind cluster after tests
	test.Testenv.Finish(
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.SlurmOperatorCRDsUninstall(config)
		},
		func(ctx context.Context, config *envconf.Config) (context.Context, error) {
			return ctx, test.SlurmOperatorUninstall(config)
		},
		envfuncs.DeleteNamespace("slinky"),
		envfuncs.DeleteNamespace("slurm"),
		envfuncs.DeleteNamespace("slurm-bridge"),
		envfuncs.DeleteNamespace("cert-manager"),
		envfuncs.DestroyCluster(kindClusterName),
	)

	// launch package tests
	os.Exit(test.Testenv.Run(m))
}

func TestInstallation(t *testing.T) {
	tests := []struct {
		name         string
		install      bool
		test         bool
		dependencies []types.Feature
	}{
		{
			name:    "Install Slurm",
			install: true,
			test:    true,
		},
	}

	for _, tt := range tests {
		steps := getFeatures(tt.install, tt.test, tt.dependencies)

		t.Run(tt.name, func(t *testing.T) {
			_ = test.Testenv.Test(t, steps...)
		})

	}
}
