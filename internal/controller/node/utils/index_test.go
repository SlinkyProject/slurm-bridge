// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
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

func resourceSlicesForIndexTest() []resourcev1.ResourceSlice {
	slices := []resourcev1.ResourceSlice{
		{ObjectMeta: metav1.ObjectMeta{Name: "slice-node0"}, Spec: resourcev1.ResourceSliceSpec{NodeName: ptr.To("kube-0")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "slice-node1"}, Spec: resourcev1.ResourceSliceSpec{NodeName: ptr.To("kube-1")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "slice-allnodes"}, Spec: resourcev1.ResourceSliceSpec{AllNodes: ptr.To(true)}},
		{ObjectMeta: metav1.ObjectMeta{Name: "slice-perdevice"}, Spec: resourcev1.ResourceSliceSpec{NodeName: ptr.To("kube-0"), PerDeviceNodeSelection: ptr.To(true)}},
	}
	for i := range slices {
		slices[i].Spec.Driver = "gpu.example.com"
		slices[i].Spec.Pool = resourcev1.ResourcePool{Name: slices[i].Name, Generation: 1, ResourceSliceCount: 1}
	}
	return slices
}

func sliceNames(slices []resourcev1.ResourceSlice) []string {
	names := make([]string, len(slices))
	for i, s := range slices {
		names[i] = s.Name
	}
	sort.Strings(names)
	return names
}

func TestGetResourceSlicesForNode_Indexed(t *testing.T) {
	slices := resourceSlicesForIndexTest()
	c := fake.NewClientBuilder().
		WithIndex(&resourcev1.ResourceSlice{}, IndexFieldResourceSliceNode, IndexResourceSliceByNode).
		WithIndex(&resourcev1.ResourceSlice{}, IndexFieldResourceSlicePool, IndexResourceSliceByPool).
		WithObjects(&slices[0], &slices[1], &slices[2], &slices[3]).
		Build()

	got, err := GetResourceSlicesForNode(context.Background(), c, "kube-0")
	if err != nil {
		t.Fatalf("GetResourceSlicesForNode() error = %v", err)
	}
	// kube-0's own slice, the AllNodes slice, and the PerDeviceNodeSelection slice
	// (which is not exact-name-scoped even though NodeName happens to be set) — but
	// not kube-1's slice.
	want := []string{"slice-allnodes", "slice-node0", "slice-perdevice"}
	if got := sliceNames(got); !equalStrings(got, want) {
		t.Errorf("GetResourceSlicesForNode() = %v, want %v", got, want)
	}
}

func TestGetResourceSlicesForNodeIncludesWholePools(t *testing.T) {
	for _, poolIndex := range []bool{true, false} {
		name := "indexed"
		if !poolIndex {
			name = "fallback without pool index"
		}
		t.Run(name, func(t *testing.T) {
			local := resourceSlicesForIndexTest()[0]
			peer := *local.DeepCopy()
			peer.Name = "peer"
			peer.Spec.NodeName = ptr.To("kube-1")
			newer := *peer.DeepCopy()
			newer.Name = "newer"
			newer.Spec.Pool.Generation = 2
			otherDriver := *peer.DeepCopy()
			otherDriver.Name = "other-driver"
			otherDriver.Spec.Driver = "dra.cpu"
			otherPool := resourceSlicesForIndexTest()[1]
			builder := fake.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, IndexFieldResourceSliceNode, IndexResourceSliceByNode).
				WithObjects(&local, &peer, &newer, &otherDriver, &otherPool)
			if poolIndex {
				builder.WithIndex(&resourcev1.ResourceSlice{}, IndexFieldResourceSlicePool, IndexResourceSliceByPool)
			}
			got, err := GetResourceSlicesForNode(context.Background(), builder.Build(), "kube-0")
			if err != nil {
				t.Fatalf("GetResourceSlicesForNode() error = %v", err)
			}
			want := []string{"newer", "peer", "slice-node0"}
			if got := sliceNames(got); !equalStrings(got, want) {
				t.Fatalf("GetResourceSlicesForNode() = %v, want %v", got, want)
			}
		})
	}
}

// A client with no registered index must fall back to a full list and still return
// every ResourceSlice (correctness over efficiency).
func TestGetResourceSlicesForNode_FallsBackWithoutIndex(t *testing.T) {
	slices := resourceSlicesForIndexTest()
	c := fake.NewClientBuilder().WithObjects(&slices[0], &slices[1], &slices[2], &slices[3]).Build()

	got, err := GetResourceSlicesForNode(context.Background(), c, "kube-0")
	if err != nil {
		t.Fatalf("GetResourceSlicesForNode() error = %v", err)
	}
	want := []string{"slice-allnodes", "slice-node0", "slice-node1", "slice-perdevice"}
	if got := sliceNames(got); !equalStrings(got, want) {
		t.Errorf("GetResourceSlicesForNode() = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
