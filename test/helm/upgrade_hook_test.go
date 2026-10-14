// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"
	"time"
)

const shutdownScript = "../../helm/slurm-bridge/files/pre-upgrade.sh"

// Exercise the actual hook script with delayed controller observations,
// terminating pods, and API failures. No Kubernetes cluster is required.
func TestUpgradeShutdown(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deployments []string
		pods        []string
		failAction  string
	}{
		{name: "already stopped", deployments: []string{""}, pods: []string{""}},
		{name: "wait for deployment", deployments: []string{"scheduler", ""}, pods: []string{"", ""}},
		{name: "wait for terminating pod", deployments: []string{"", ""}, pods: []string{"pod/old", ""}},
		{name: "wait for both", deployments: []string{"scheduler", "", ""}, pods: []string{"pod/old", "pod/old", ""}},
		{name: "scale failure", failAction: "scale deployments"},
		{name: "deployment read failure", failAction: "get deployments"},
		{name: "pod read failure", deployments: []string{""}, failAction: "get pods"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, content string) {
				t.Helper()
				// Test fixtures include executable kubectl and sleep stubs in a private directory.
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o700); err != nil { // #nosec G306
					t.Fatal(err)
				}
			}
			write("round", "0")
			write("sleep", "#!/bin/sh\nexit 0\n")
			write("kubectl", `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$HOOK_TEST_DIR/calls"
shift 3
action="$1 $2"
[ "$action" != "$FAIL_ACTION" ] || exit 1
if [ "$action" = "scale deployments" ]; then
    exit 0
fi
round=$(cat "$HOOK_TEST_DIR/round")
cat "$HOOK_TEST_DIR/$2-$round"
if [ "$2" = "pods" ]; then
    echo "$((round + 1))" > "$HOOK_TEST_DIR/round"
fi
`)
			for i, response := range tc.deployments {
				write(fmt.Sprintf("deployments-%d", i), response)
			}
			for i, response := range tc.pods {
				write(fmt.Sprintf("pods-%d", i), response)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", shutdownScript)
			selector := "app.kubernetes.io/instance=test-release,app.kubernetes.io/component in (scheduler,controllers,admission)"
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"),
				"HOOK_TEST_DIR="+dir, "FAIL_ACTION="+tc.failAction,
				"BRIDGE_NAMESPACE=test-namespace", "BRIDGE_SELECTOR="+selector)
			output, err := cmd.CombinedOutput()
			if (err != nil) != (tc.failAction != "") || ctx.Err() != nil {
				t.Fatalf("unexpected result: %v\n%s", err, output)
			}
			if strings.Contains(string(output), "All Slurm Bridge components have stopped.") != (tc.failAction == "") {
				t.Fatalf("incorrect shutdown result: %s", output)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if !strings.Contains(lines[0], "scale deployments") || !strings.Contains(lines[0], "--replicas=0") {
				t.Fatalf("did not scale before waiting: %s", calls)
			}
			for _, call := range lines {
				if !strings.Contains(call, "--namespace=test-namespace") || !strings.Contains(call, "--selector="+selector) {
					t.Fatalf("unscoped API call: %s", call)
				}
			}
			if tc.failAction == "" && len(lines) != 1+2*len(tc.pods) {
				t.Fatalf("unexpected number of polls: %s", calls)
			}
		})
	}
}

func TestUpgradeDeploymentObservation(t *testing.T) {
	script, err := os.ReadFile(shutdownScript)
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`go-template='([^']+)'`).FindSubmatch(script)
	if len(match) != 2 {
		t.Fatal("missing Deployment observation template")
	}
	format, err := template.New("deployments").Parse(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		spec    string
		status  string
		pending bool
	}{
		{"stopped with omitted replicas", `{"replicas":0}`, `{"observedGeneration":2}`, false},
		{"stopped with zero replicas", `{"replicas":0}`, `{"observedGeneration":2,"replicas":0}`, false},
		{"unobserved generation", `{"replicas":0}`, `{"observedGeneration":1}`, true},
		{"missing status", `{"replicas":0}`, `{}`, true},
		{"remaining replicas", `{"replicas":0}`, `{"observedGeneration":2,"replicas":1}`, true},
		{"external scale up", `{"replicas":1}`, `{"observedGeneration":2}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var object any
			data := fmt.Sprintf(`{"items":[{"metadata":{"name":"scheduler","generation":2},"spec":%s,"status":%s}]}`, tc.spec, tc.status)
			if err := json.Unmarshal([]byte(data), &object); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := format.Execute(&output, object); err != nil {
				t.Fatal(err)
			}
			if (output.Len() > 0) != tc.pending {
				t.Fatalf("pending = %q, want pending = %t", output.String(), tc.pending)
			}
		})
	}
}
