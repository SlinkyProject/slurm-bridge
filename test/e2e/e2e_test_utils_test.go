// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"maps"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestParseSlurmNodeMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []slurmNodeMode{slurmNodeModeExternal, slurmNodeModeHybrid} {
		mode := mode
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			got, err := parseSlurmNodeMode(string(mode))
			if err != nil {
				t.Fatalf("parseSlurmNodeMode() error = %v", err)
			}
			if got != mode {
				t.Fatalf("parseSlurmNodeMode() = %q, want %q", got, mode)
			}
		})
	}

	if _, err := parseSlurmNodeMode(""); err == nil {
		t.Fatal("parseSlurmNodeMode() accepted an empty mode")
	}
}

func TestParseE2ECleanupFromEnvironment(t *testing.T) {
	t.Setenv(e2eCleanupEnvironment, "")
	enabled, err := parseE2ECleanupFromEnvironment()
	if err != nil {
		t.Fatalf("parseE2ECleanupFromEnvironment() error = %v", err)
	}
	if !enabled {
		t.Fatal("parseE2ECleanupFromEnvironment() = false by default, want true")
	}

	t.Setenv(e2eCleanupEnvironment, "false")
	enabled, err = parseE2ECleanupFromEnvironment()
	if err != nil {
		t.Fatalf("parseE2ECleanupFromEnvironment() error = %v", err)
	}
	if enabled {
		t.Fatal("parseE2ECleanupFromEnvironment() = true, want false")
	}

	t.Setenv(e2eCleanupEnvironment, "invalid")
	if _, err := parseE2ECleanupFromEnvironment(); err == nil {
		t.Fatal("parseE2ECleanupFromEnvironment() accepted an invalid value")
	}
}

func TestSlurmNodeStates(t *testing.T) {
	t.Parallel()

	output := `NodeName=worker-1 CPUTot=8 State=IDLE+EXTERNAL
NodeName=worker-2 CPUTot=8 State=ALLOCATED
`
	want := map[string]string{
		"worker-1": "IDLE+EXTERNAL",
		"worker-2": "ALLOCATED",
	}

	if got := slurmNodeStates(output); !maps.Equal(got, want) {
		t.Fatalf("slurmNodeStates() = %v, want %v", got, want)
	}
}

func TestSlurmJobNodeList(t *testing.T) {
	t.Parallel()

	got, err := slurmJobNodeList("JobId=42 JobState=RUNNING NodeList=worker-2")
	if err != nil {
		t.Fatalf("slurmJobNodeList() error = %v", err)
	}
	if got != "worker-2" {
		t.Fatalf("slurmJobNodeList() = %q, want %q", got, "worker-2")
	}
	if _, err := slurmJobNodeList("JobId=42 JobState=PENDING NodeList=(null)"); err == nil {
		t.Fatal("slurmJobNodeList() accepted an unallocated job")
	}
	state, err := slurmJobField("JobId=42 JobState=COMPLETED NodeList=worker-2", "JobState")
	if err != nil {
		t.Fatalf("slurmJobField() error = %v", err)
	}
	if state != "COMPLETED" {
		t.Fatalf("slurmJobField() = %q, want %q", state, "COMPLETED")
	}
}

func TestBridgeNodesReadyForMode(t *testing.T) {
	t.Parallel()

	externalNode := corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker-1",
	}}
	if ready, observation := bridgeNodesReadyForMode(
		slurmNodeModeExternal,
		[]corev1.Node{externalNode},
		map[string]string{"worker-1": "IDLE+EXTERNAL"},
		nil,
	); !ready {
		t.Fatalf("external node was not ready: %s", observation)
	}

	hybridNode := corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "worker-2", Labels: map[string]string{wellknown.LabelSlurmNodeName: "slurm-bridge-0"},
	}}
	if ready, observation := bridgeNodesReadyForMode(
		slurmNodeModeHybrid,
		[]corev1.Node{hybridNode},
		map[string]string{"slurm-bridge-0": "IDLE"},
		map[string]struct{}{"worker-2": {}},
	); !ready {
		t.Fatalf("hybrid node was not ready: %s", observation)
	}

	if ready, _ := bridgeNodesReadyForMode(
		slurmNodeModeHybrid,
		[]corev1.Node{externalNode},
		map[string]string{"worker-1": "IDLE+EXTERNAL"},
		map[string]struct{}{"worker-1": {}},
	); ready {
		t.Fatal("hybrid readiness accepted an external node")
	}
}

func TestReadyHybridWorkerNodes(t *testing.T) {
	t.Parallel()

	pods := []corev1.Pod{
		{
			Spec: corev1.PodSpec{NodeName: "worker-1"},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			}},
		},
		{
			Spec: corev1.PodSpec{NodeName: "worker-2"},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			}},
		},
	}
	want := map[string]struct{}{"worker-1": {}}
	if got := readyHybridWorkerNodes(pods); !maps.Equal(got, want) {
		t.Fatalf("readyHybridWorkerNodes() = %v, want %v", got, want)
	}
}

func TestHybridSlurmBatchSchedulingLabel(t *testing.T) {
	t.Parallel()

	feature := testHybridSlurmBatchScheduling()
	if !feature.Labels().Contains(slurmNodeModeLabel, string(slurmNodeModeHybrid)) {
		t.Fatalf("feature labels %v do not identify a hybrid-only test", feature.Labels())
	}
}
