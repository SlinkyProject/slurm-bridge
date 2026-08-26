// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
)

const (
	defaultE2EArtifactsDir = "e2e-artifacts"
	diagnosticTimeout      = 20 * time.Second
)

// captureFailureDiagnostics writes a compact cluster snapshot before a failed
// feature's teardown removes the workload under test.
func captureFailureDiagnostics(t *testing.T, failure string, namespaces ...string) {
	t.Helper()

	artifactRoot := os.Getenv("E2E_ARTIFACTS_DIR")
	if artifactRoot == "" {
		artifactRoot = filepath.Join(repositoryRoot(), defaultE2EArtifactsDir)
	}

	dir := filepath.Join(artifactRoot, "failures", artifactName(t.Name()))
	// E2E_ARTIFACTS_DIR intentionally allows CI to choose the artifact root.
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec
		t.Logf("failed to create E2E diagnostic directory %q: %v", dir, err)
		return
	}

	summary := fmt.Sprintf(
		"captured: %s\ntest: %s\nfailure: %s\n",
		time.Now().UTC().Format(time.RFC3339Nano),
		t.Name(),
		failure,
	)
	writeDiagnosticFile(t, filepath.Join(dir, "summary.txt"), []byte(summary))

	captureKubectl(t, dir, "cluster-nodes.txt", "get", "nodes", "-o", "wide")
	captureKubectl(t, dir, "cluster-pods.txt", "get", "pods", "--all-namespaces", "-o", "wide")
	captureKubectl(t, dir, "dra-resources.yaml", "get",
		"deviceclasses.resource.k8s.io,resourceslices.resource.k8s.io", "-o", "yaml")

	for _, namespace := range uniqueStrings(namespaces) {
		if namespace == "" {
			continue
		}

		namespaceDir := filepath.Join(dir, artifactName(namespace))
		if err := os.MkdirAll(namespaceDir, 0o755); err != nil { //nolint:gosec // namespace is reduced to a safe artifact name
			t.Logf("failed to create namespace diagnostic directory %q: %v", namespaceDir, err)
			continue
		}

		captureKubectl(t, namespaceDir, "workloads.txt", "get",
			"pods,jobs,deployments,statefulsets,daemonsets,services,resourceclaims.resource.k8s.io",
			"--namespace", namespace, "-o", "wide")
		captureKubectl(t, namespaceDir, "events.txt", "get", "events",
			"--namespace", namespace, "--sort-by=.metadata.creationTimestamp")
		captureKubectl(t, namespaceDir, "pods-describe.txt", "describe", "pods", "--namespace", namespace)
		capturePodLogs(t, namespaceDir, namespace)

		if namespace == "slurm" {
			captureKubectl(t, namespaceDir, "slurm-jobs.txt", "exec", "--namespace", namespace,
				"slurm-controller-0", "--", "scontrol", "show", "jobs", "--details")
			captureKubectl(t, namespaceDir, "slurm-nodes.txt", "exec", "--namespace", namespace,
				"slurm-controller-0", "--", "scontrol", "show", "nodes", "--details")
		}
	}

	t.Logf("E2E failure diagnostics written to %s", dir)
}

func capturePodLogs(t *testing.T, dir, namespace string) {
	t.Helper()

	output, err := runKubectl("get", "pods", "--namespace", namespace, "-o", "name")
	if err != nil {
		writeDiagnosticFile(t, filepath.Join(dir, "pod-logs-error.txt"), diagnosticOutput(err, output))
		return
	}

	for _, pod := range strings.Fields(string(output)) {
		name := artifactName(strings.TrimPrefix(pod, "pod/"))
		captureKubectl(t, dir, name+".log", "logs", "--namespace", namespace, pod,
			"--all-containers=true", "--prefix=true", "--timestamps=true", "--tail=500")
		captureKubectl(t, dir, name+"-previous.log", "logs", "--namespace", namespace, pod,
			"--all-containers=true", "--prefix=true", "--timestamps=true", "--tail=500", "--previous")
	}
}

func captureKubectl(t *testing.T, dir, filename string, args ...string) {
	t.Helper()

	output, err := runKubectl(args...)
	header := []byte("$ kubectl " + strings.Join(args, " ") + "\n")
	writeDiagnosticFile(t, filepath.Join(dir, filename), append(header, diagnosticOutput(err, output)...))
}

func runKubectl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), diagnosticTimeout)
	defer cancel()

	return exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
}

func diagnosticOutput(err error, output []byte) []byte {
	if err == nil {
		return output
	}
	return []byte(fmt.Sprintf("error: %v\n%s", err, output))
}

func writeDiagnosticFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // path is rooted in the configured artifact directory
		t.Logf("failed to write E2E diagnostic file %q: %v", path, err)
	}
}

func artifactName(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, value)

	value = strings.Trim(value, "._-")
	if value == "" {
		return "unnamed"
	}
	return value
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func statusJSON(status any) string {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Sprintf("<failed to marshal status: %v>", err)
	}
	return string(data)
}

func repositoryRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
