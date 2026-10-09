// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestE2ESuiteSelection(t *testing.T) {
	for _, tc := range []struct {
		name, labels, suite   string
		requireLabel, wantRun bool
	}{
		{"default scheduling", "", "scheduling", false, true},
		{"default skips upgrade", "", "upgrade", true, false},
		{"upgrade selected", "suite=upgrade", "upgrade", true, true},
		{"upgrade skips scheduling", "suite=upgrade", "scheduling", false, false},
		{"scheduling skips upgrade", "suite=scheduling", "upgrade", true, false},
		{"other labels do not opt in", "workload-api=v1beta1", "upgrade", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("E2E_LABELS", tc.labels)
			ran := false
			t.Run("suite", func(t *testing.T) {
				requireE2ESuite(t, tc.suite, tc.requireLabel)
				ran = true
			})
			if ran != tc.wantRun {
				t.Fatalf("suite executed=%t, want %t", ran, tc.wantRun)
			}
		})
	}
	t.Setenv("E2E_LABELS", "upgrade")
	if _, err := parseE2ELabels(); err == nil {
		t.Fatal("accepted malformed feature labels")
	}
}

func TestUpgradeTestVersions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, from, to, wantError string
	}{
		{name: "patch", from: "1.2.2", to: "1.2.3"},
		{name: "next minor", from: "1.2.3", to: "1.3.0"},
		{name: "release candidate", from: "1.2.3", to: "1.3.0-rc1"},
		{name: "candidate to release", from: "1.3.0-rc1", to: "1.3.0"},
		{name: "skipped minor", from: "1.1.9", to: "1.3.0-rc1", wantError: "skipping minor releases"},
		{name: "same version", from: "1.3.0", to: "1.3.0", wantError: "must be newer"},
		{name: "downgrade", from: "1.3.0", to: "1.2.3", wantError: "must be newer"},
		{name: "major change", from: "1.3.0", to: "2.0.0", wantError: "within one major version"},
		{name: "invalid baseline", from: "latest", to: "1.3.0", wantError: "parse baseline version"},
		{name: "invalid target", from: "1.2.3", to: "latest", wantError: "parse target version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUpgradeTestVersions(tc.from, tc.to)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestUpgradePodPreserved(t *testing.T) {
	t.Parallel()
	before := slurmTestPod(slurmBridgeNamespace, "running", nil)
	before.UID = "original"
	before.Spec.NodeName = "worker-1"
	before.Labels = map[string]string{slurmJobIDLabel: "42"}
	before.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "slurm-worker-1"}
	before.Status = corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{
			ContainerID: "containerd://original",
			State:       corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	}
	if err := upgradePodPreserved(before, before.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*corev1.Pod){
		"recreated pod":      func(p *corev1.Pod) { p.UID = "replacement" },
		"new allocation":     func(p *corev1.Pod) { p.Labels[slurmJobIDLabel] = "43" },
		"different node":     func(p *corev1.Pod) { p.Spec.NodeName = "worker-2" },
		"changed assignment": func(p *corev1.Pod) { p.Annotations[wellknown.AnnotationExternalJobNode] = "other" },
		"restarted":          func(p *corev1.Pod) { p.Status.ContainerStatuses[0].RestartCount++ },
		"new container":      func(p *corev1.Pod) { p.Status.ContainerStatuses[0].ContainerID = "new" },
		"lost status":        func(p *corev1.Pod) { p.Status.ContainerStatuses = nil },
		"terminated":         func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed },
		"being deleted":      func(p *corev1.Pod) { now := metav1.Now(); p.DeletionTimestamp = &now },
	} {
		t.Run(name, func(t *testing.T) {
			after := before.DeepCopy()
			mutate(after)
			if err := upgradePodPreserved(before, after); err == nil {
				t.Fatal("accepted a workload disrupted by the upgrade")
			}
		})
	}
}

func TestUpgradeHelmUsesSelectedContext(t *testing.T) {
	dir := t.TempDir()
	command := "#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'warning\\n' >&2\n"
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(command), 0o700); err != nil { //nolint:gosec // Executable test stub isolated in t.TempDir.
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(e2eKubeContextEnvironment, "upgrade-cluster")
	output := runUpgradeHelm(context.Background(), t, "get", "metadata", "slurm-bridge")
	want := "--kube-context\nupgrade-cluster\n--namespace\nslurm\nget\nmetadata\nslurm-bridge\n"
	if string(output) != want || strings.Contains(string(output), "warning") {
		t.Fatalf("unexpected Helm arguments or mixed stderr: %q", output)
	}
}

func TestUpgradeDeployUsesSelectedContext(t *testing.T) {
	dir := t.TempDir()
	command := "#!/bin/sh\nprintf '%s\\n' \"$SKAFFOLD_KUBE_CONTEXT\" \"$PWD\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "make"), []byte(command), 0o700); err != nil { //nolint:gosec // Executable test stub isolated in t.TempDir.
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SKAFFOLD_KUBE_CONTEXT", "unrelated-cluster")
	t.Setenv(e2eKubeContextEnvironment, "upgrade-cluster")
	output := runUpgradeCommand(context.Background(), t, "make", "deploy")
	want := "upgrade-cluster\n" + repositoryRoot() + "\ndeploy\n"
	if string(output) != want {
		t.Fatalf("deployment did not use the selected cluster and checkout: %q", output)
	}
}
