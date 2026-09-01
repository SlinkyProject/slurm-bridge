// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func nodesForIndexTest() []corev1.Node {
	return []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "kube-0"}},
		{ObjectMeta: metav1.ObjectMeta{
			Name:   "kube-1",
			Labels: map[string]string{wellknown.LabelSlurmNodeName: "slurm-1"},
		}},
	}
}

func TestGetNodeNameForSlurmName_Indexed(t *testing.T) {
	nodes := nodesForIndexTest()
	c := fake.NewClientBuilder().
		WithIndex(&corev1.Node{}, IndexFieldSlurmNodeName, IndexNodeBySlurmName).
		WithObjects(&nodes[0], &nodes[1]).
		Build()

	name, ok, err := GetNodeNameForSlurmName(context.Background(), c, "slurm-1")
	if err != nil {
		t.Fatalf("GetNodeNameForSlurmName() error = %v", err)
	}
	if !ok || name != "kube-1" {
		t.Errorf("GetNodeNameForSlurmName() = (%q, %v), want (\"kube-1\", true)", name, ok)
	}

	name, ok, err = GetNodeNameForSlurmName(context.Background(), c, "kube-0")
	if err != nil {
		t.Fatalf("GetNodeNameForSlurmName() error = %v", err)
	}
	if !ok || name != "kube-0" {
		t.Errorf("GetNodeNameForSlurmName() = (%q, %v), want (\"kube-0\", true)", name, ok)
	}

	_, ok, err = GetNodeNameForSlurmName(context.Background(), c, "no-such-node")
	if err != nil {
		t.Fatalf("GetNodeNameForSlurmName() error = %v", err)
	}
	if ok {
		t.Errorf("GetNodeNameForSlurmName() ok = true, want false for unknown Slurm name")
	}
}

// A client with no registered index (e.g. a direct API-server client, not a manager's
// indexed cache) must fall back to a full list scan and still resolve correctly.
func TestGetNodeNameForSlurmName_FallsBackWithoutIndex(t *testing.T) {
	nodes := nodesForIndexTest()
	c := fake.NewClientBuilder().WithObjects(&nodes[0], &nodes[1]).Build()

	name, ok, err := GetNodeNameForSlurmName(context.Background(), c, "slurm-1")
	if err != nil {
		t.Fatalf("GetNodeNameForSlurmName() error = %v", err)
	}
	if !ok || name != "kube-1" {
		t.Errorf("GetNodeNameForSlurmName() = (%q, %v), want (\"kube-1\", true)", name, ok)
	}
}
