// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	// "os/exec"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/test"

	// crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

// Slinky Components Installation

func installSlurmBridge() types.Feature {
	return features.New("Helm install slurm-bridge").
		Setup(test.DoSlurmBridgeInstall).
		Assess("Slurm Bridge Controllers Are Running Successfully", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			_, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("Failed to get new controller-runtime client: %v", err)
			}
			return test.CheckDeploymentStatus(ctx, t, config, "slurm-bridge-controllers", test.SlinkyNamespace)
		}).
		Assess("Slurm Bridge Admission Is Running Successfully", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			_, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("Failed to get new controller-runtime client: %v", err)
			}
			return test.CheckDeploymentStatus(ctx, t, config, "slurm-bridge-admission", test.SlinkyNamespace)
		}).
		Assess("Slurm Bridge Scheduler Is Running Successfully", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			_, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("Failed to get new controller-runtime client: %v", err)
			}
			return test.CheckDeploymentStatus(ctx, t, config, "slurm-bridge-scheduler", test.SlinkyNamespace)
		}).Feature()
}
