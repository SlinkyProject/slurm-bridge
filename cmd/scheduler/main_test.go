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
	schedulercommand "github.com/SlinkyProject/slurm-bridge/internal/scheduler/command"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestMain(m *testing.M) {
	rc := m.Run()
	os.Exit(rc)
}

func TestGenericWorkloadEnabledByDefaultAndOptOut(t *testing.T) {
	if !utilfeature.DefaultFeatureGate.Enabled(kubefeatures.GenericWorkload) {
		t.Fatal("native group scheduling must be enabled by default")
	}
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, true)
	command := schedulercommand.New(app.WithPlugin(slurmbridge.Name, slurmbridge.New))
	if err := command.ParseFlags([]string{"--feature-gates=GenericWorkload=false"}); err != nil {
		t.Fatal(err)
	}
	if err := command.PersistentPreRunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if utilfeature.DefaultFeatureGate.Enabled(kubefeatures.GenericWorkload) {
		t.Fatal("GenericWorkload=false must disable native scheduling")
	}
	if api, err := slurmjobir.NewWorkloadAPI(nil, nil); err != nil || api != nil {
		t.Fatalf("GenericWorkload=false must skip bridge API discovery: api=%v err=%v", api, err)
	}
}
