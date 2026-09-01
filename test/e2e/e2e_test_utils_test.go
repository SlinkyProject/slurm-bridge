// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"maps"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"

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

func TestParseMockNVMLFromEnvironment(t *testing.T) {
	t.Setenv(mockNVMLEnvironment, "true")
	enabled, err := parseMockNVMLFromEnvironment()
	if err != nil {
		t.Fatalf("parseMockNVMLFromEnvironment() error = %v", err)
	}
	if !enabled {
		t.Fatal("parseMockNVMLFromEnvironment() = false, want true")
	}

	t.Setenv(mockNVMLEnvironment, "invalid")
	if _, err := parseMockNVMLFromEnvironment(); err == nil {
		t.Fatal("parseMockNVMLFromEnvironment() accepted an invalid value")
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
		Name:   "worker-1",
		Labels: map[string]string{wellknown.LabelExternalNode: "true"},
	}}
	if ready, observation := bridgeNodesReadyForMode(
		slurmNodeModeExternal,
		[]corev1.Node{externalNode},
		map[string]string{"worker-1": "IDLE+EXTERNAL"},
		nil,
	); !ready {
		t.Fatalf("external node was not ready: %s", observation)
	}

	hybridNode := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-2"}}
	if ready, observation := bridgeNodesReadyForMode(
		slurmNodeModeHybrid,
		[]corev1.Node{hybridNode},
		map[string]string{"worker-2": "IDLE"},
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

func TestDRACPUSetFromEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment string
		want        cpuset.CPUSet
		wantErr     string
	}{
		{
			name:        "exclusive allocation",
			environment: "PATH=/bin\nDRA_CPUSET_claim=0-7\n",
			want:        cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
		},
		{
			name:        "non-contiguous allocation",
			environment: "DRA_CPUSET_claim=0-2,6\n",
			want:        cpuset.New(0, 1, 2, 6),
		},
		{
			name:        "missing allocation",
			environment: "PATH=/bin\n",
			wantErr:     "was not injected",
		},
		{
			name:        "multiple allocations",
			environment: "DRA_CPUSET_first=0\nDRA_CPUSET_second=1\n",
			wantErr:     "multiple DRA CPU allocations",
		},
		{
			name:        "invalid allocation",
			environment: "DRA_CPUSET_claim=not-a-cpuset\n",
			wantErr:     "parse allocated CPU set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := draCPUSetFromEnvironment(tt.environment)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("draCPUSetFromEnvironment() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("draCPUSetFromEnvironment() error = %v", err)
			}
			if !got.Equals(tt.want) {
				t.Fatalf("draCPUSetFromEnvironment() = %s, want %s", got.String(), tt.want.String())
			}
		})
	}
}
