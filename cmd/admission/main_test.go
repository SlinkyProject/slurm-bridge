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
)

func TestMain(m *testing.M) {
	rc := m.Run()
	os.Exit(rc)
}

func TestLegacyFeatureGateFlags(t *testing.T) {
	if !utilfeature.DefaultFeatureGate.Enabled(features.EnableLegacyv1alpha1PodGroups) ||
		!utilfeature.DefaultFeatureGate.Enabled(features.EnableLegacyLWSGangs) {
		t.Fatal("legacy workload support must default to enabled")
	}
	for _, tt := range []struct {
		name          string
		args          []string
		wantPodGroups bool
		wantLWS       bool
	}{
		{name: "defaults", wantPodGroups: true, wantLWS: true},
		{
			name: "disable legacy PodGroups", wantLWS: true,
			args: []string{"--feature-gates=EnableLegacyv1alpha1PodGroups=false"},
		},
		{
			name: "disable legacy LWS gangs", wantPodGroups: true,
			args: []string{"--feature-gates=EnableLegacyLWSGangs=false"},
		},
		{
			name: "disable both",
			args: []string{"--feature-gates=EnableLegacyv1alpha1PodGroups=false,EnableLegacyLWSGangs=false"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableLegacyv1alpha1PodGroups, true)
			featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableLegacyLWSGangs, true)
			fs := flag.NewFlagSet(tt.name, flag.ContinueOnError)
			bindFlags(fs, &Flags{})
			if err := fs.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			if got := utilfeature.DefaultFeatureGate.Enabled(features.EnableLegacyv1alpha1PodGroups); got != tt.wantPodGroups {
				t.Errorf("EnableLegacyv1alpha1PodGroups = %t, want %t", got, tt.wantPodGroups)
			}
			if got := utilfeature.DefaultFeatureGate.Enabled(features.EnableLegacyLWSGangs); got != tt.wantLWS {
				t.Errorf("EnableLegacyLWSGangs = %t, want %t", got, tt.wantLWS)
			}
		})
	}
}
