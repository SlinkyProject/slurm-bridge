// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"k8s.io/component-base/cli"
	_ "k8s.io/component-base/metrics/prometheus/clientgo" // for rest client metric registration
	_ "k8s.io/component-base/metrics/prometheus/version"  // for version metric registration
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	// Ensure scheme package is initialized.
	_ "sigs.k8s.io/scheduler-plugins/apis/config/scheme"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge"
)

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create;get;list;update

func main() {
	// Register custom plugins to the scheduler framework.
	// Later they can consist of scheduler profile(s) and hence
	// used by various kinds of workloads.
	command := app.NewSchedulerCommand(
		app.WithPlugin(slurmbridge.Name, slurmbridge.New),
	)
	// kube-scheduler's own command already owns "--config" for the KubeSchedulerConfiguration
	// file, so slurm-bridge's own config file path needs a distinct flag name.
	command.Flags().StringVar(&slurmbridge.ConfigFile, "slurm-bridge-config", slurmbridge.ConfigFile,
		"Path to the slurm-bridge config file.")
	code := cli.Run(command)
	os.Exit(code)
}
