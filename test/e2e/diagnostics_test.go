// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFailureDiagnosticsUseSelectedContext(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "kubectl-commands.txt")
	// Record every invocation, including pod discovery and commands inside pods.
	kubectl := `#!/bin/sh
printf '%s ' "$@" >> "$KUBECTL_TRACE"
printf '\n' >> "$KUBECTL_TRACE"
case " $* " in
  *" get pods "*" -o name "*) printf 'pod/diagnostic-probe\n' ;;
esac
`
	// The mock kubectl must be executable and is isolated in t.TempDir().
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0o700); err != nil { // #nosec G306
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KUBECTL_TRACE", trace)
	t.Setenv(e2eKubeContextEnvironment, "e2e-selected")
	t.Setenv("E2E_ARTIFACTS_DIR", dir)

	captureFailureDiagnostics(t, "context regression", "slurm")

	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, command := range commands {
		if !strings.HasPrefix(command, "--context e2e-selected ") {
			t.Errorf("diagnostic command can fall back to the current context: %s", command)
		}
	}
	for _, want := range []string{
		"get pods --namespace slurm -o name",
		"logs --namespace slurm pod/diagnostic-probe",
		"--previous",
		"scontrol show jobs --details",
		"scontrol show nodes --details",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("diagnostics did not invoke %q", want)
		}
	}

	artifactDir := filepath.Join(dir, "failures", artifactName(t.Name()), "slurm")
	for _, filename := range []string{"slurm-jobs.txt", "diagnostic-probe.log"} {
		artifact, err := os.ReadFile(filepath.Join(artifactDir, filename))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(artifact), "$ kubectl --context e2e-selected ") {
			t.Errorf("%s does not record the selected context: %s", filename, artifact)
		}
	}
}

func TestRunKubectlRequiresContext(t *testing.T) {
	t.Setenv(e2eKubeContextEnvironment, "")
	t.Setenv("PATH", t.TempDir())

	if _, err := runKubectl("get", "nodes"); err == nil || !strings.Contains(err.Error(), e2eKubeContextEnvironment) {
		t.Fatalf("runKubectl() error = %v, want a missing context error", err)
	}
}

func TestArtifactName(t *testing.T) {
	t.Parallel()

	if got, want := artifactName("TestScheduling/DRA resources"), "TestScheduling_DRA_resources"; got != want {
		t.Fatalf("artifactName() = %q, want %q", got, want)
	}
	if got, want := artifactName("///"), "unnamed"; got != want {
		t.Fatalf("artifactName() = %q, want %q", got, want)
	}
}

func TestUniqueStrings(t *testing.T) {
	t.Parallel()

	got := uniqueStrings([]string{"slurm", "slurm-bridge", "slurm", ""})
	want := []string{"slurm", "slurm-bridge", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueStrings() = %v, want %v", got, want)
	}
}
