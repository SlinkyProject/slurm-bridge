// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package test

import "sigs.k8s.io/e2e-framework/pkg/env"

var (
	Testenv              env.Environment
	TestUID              string
	SlinkyNamespace      string = "slinky"
	SlurmNamespace       string = "slurm"
	SlurmBridgeNamespace string = "slurm-bridge"
	Basepath             string
)

const (
	ImageScheduler   = "ghcr.io/slinkyproject/slurm-bridge:e2e"
	ImageControllers = "ghcr.io/slinkyproject/slurm-controllers:e2e"
	ImageAdmission   = "ghcr.io/slinkyproject/slurm-admission:e2e"
)
