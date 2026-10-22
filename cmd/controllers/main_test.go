// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"os"
	"testing"

	utilfeature "k8s.io/apiserver/pkg/util/feature"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestMain(m *testing.M) {
	rc := m.Run()
	os.Exit(rc)
}

func TestControllerSchemeSupportsWorkloadAdapter(t *testing.T) {
	// Exercise the production scheme: registering the full client-go scheme
	// collides with the version-independent PodGroup type at startup.
	if _, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, slurmjobir.WorkloadAPIVersionV1Beta1); err != nil {
		t.Fatal(err)
	}
}

func TestControllerWorkloadFeatureGateFlag(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, true)
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() {
		flag.CommandLine, os.Args = oldFlags, oldArgs
	})
	flag.CommandLine = flag.NewFlagSet("controllers", flag.ContinueOnError)
	os.Args = []string{"controllers", "--feature-gates=GenericWorkload=false"}
	parseFlags(&Flags{})
	if utilfeature.DefaultFeatureGate.Enabled(features.GenericWorkload) {
		t.Fatal("controller flag did not disable native Workload support")
	}
}
