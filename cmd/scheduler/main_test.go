// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	utilfeature "k8s.io/apiserver/pkg/util/feature"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	kubefeatures "k8s.io/kubernetes/pkg/features"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge"
)

func TestMain(m *testing.M) {
	rc := m.Run()
	os.Exit(rc)
}

func TestSlurmBridgeGenericWorkloadFlagIsIndependent(t *testing.T) {
	if !utilfeature.DefaultFeatureGate.Enabled(features.SlurmBridgeGenericWorkload) {
		t.Fatal("bridge Workload support must default to enabled")
	}
	if utilfeature.DefaultFeatureGate.Enabled(kubefeatures.GenericWorkload) {
		t.Fatal("bridge gate must not enable the embedded scheduler's GenericWorkload")
	}
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.SlurmBridgeGenericWorkload, true)
	command := app.NewSchedulerCommand(app.WithPlugin(slurmbridge.Name, slurmbridge.New))
	if err := command.ParseFlags([]string{"--feature-gates=SlurmBridgeGenericWorkload=false"}); err != nil {
		t.Fatal(err)
	}
	if err := command.PersistentPreRunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if utilfeature.DefaultFeatureGate.Enabled(features.SlurmBridgeGenericWorkload) {
		t.Fatal("scheduler flag did not disable bridge Workload support")
	}
	if utilfeature.DefaultFeatureGate.Enabled(kubefeatures.GenericWorkload) {
		t.Fatal("bridge flag changed the embedded scheduler's GenericWorkload gate")
	}
}
