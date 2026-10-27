// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
)

func testNodeNUMAResourceSlice(nodeName string) *resourcev1.ResourceSlice {
	slice := testNodeCPUResourceSlice(nodeName)
	device := func(numa int64, cpus string) resourcev1.Device {
		return resourcev1.Device{Name: "cpudevnuma" + cpus, AllowMultipleAllocations: ptr.To(true), Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To(numa)}, nodeinfo.DraDriverCpu_NUMANodeID: {IntValue: ptr.To(numa)},
			nodeinfo.DraDriverCpu_SMTEnabled: {BoolValue: ptr.To(false)}, nodeinfo.DraDriverCpu_CPUIDs: {StringValue: ptr.To(cpus)},
			nodeinfo.DraDriverCpu_SMTLayout: {StringValue: ptr.To("0-3")},
		}, Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{nodeinfo.DraDriverCpu_Capacity: {Value: resource.MustParse("2")}}}
	}
	slice.Spec.Devices = []resourcev1.Device{device(0, "0-1"), device(1, "2-3")}
	return slice
}

func TestNUMARegistrationBindsGRESToCores(t *testing.T) {
	info := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*testNodeNUMAResourceSlice("worker")})
	gpu0 := structured.MakeDeviceID("gpu.example.com", "pool-a", "gpu-0")
	gpu1 := structured.MakeDeviceID("gpu.example.com", "pool-a", "gpu-1")
	inventory := []dra.GRESInventory{{
		GRES:      dra.GRES{Name: "gpu", Type: "gpu.example.com"},
		Devices:   []dra.DeviceIdentity{gpu0, gpu1},
		NUMANodes: map[dra.DeviceIdentity][]int{gpu0: {0}, gpu1: {1}},
	}}

	aligned, err := buildNodeGRESConfig(inventory, info)
	if err != nil {
		t.Fatal(err)
	}
	wantConf := "count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-0,cpus=0-1+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-1,cpus=2-3"
	if aligned.gresConf != wantConf {
		t.Fatalf("GresConf = %q, want %q", aligned.gresConf, wantConf)
	}
	unaligned, err := buildNodeGRESConfig(inventory, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name         string
		registered   nodeGRESConfig
		wantRecreate bool
	}{
		{name: "registered with NUMA cores", registered: aligned},
		{name: "registered before NUMA alignment", registered: unaligned, wantRecreate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra, err := info.EncodeApplied(tc.registered.extra)
			if err != nil {
				t.Fatal(err)
			}
			slurmNode := &api.V0044Node{Name: ptr.To("worker"), Cpus: ptr.To[int32](4), Sockets: ptr.To[int32](2), Cores: ptr.To[int32](2), Threads: ptr.To[int32](1),
				RealMemory: ptr.To[int64](0), Gres: ptr.To(tc.registered.gres), Extra: ptr.To(extra), State: ptr.To([]api.V0044NodeState{api.V0044NodeStateEXTERNAL})}
			r := &realSlurmControl{Client: fake.NewClientBuilder().WithObjects(slurmNode).Build()}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
			got, err := r.NodeNeedsRecreate(context.Background(), node, info, inventory)
			if err != nil || got != tc.wantRecreate {
				t.Fatalf("NodeNeedsRecreate() = %v, %v; want %v", got, err, tc.wantRecreate)
			}
		})
	}
}

func TestGRESEqualIgnoresSocketAffinity(t *testing.T) {
	if !gresEqual("gpu:gpu.nvidia.com:8,nic:dranet0:1", "nic:dranet0:1(S:1),gpu:gpu.nvidia.com:8(S:0-1)") {
		t.Fatal("socket affinity suffix changed the GRES comparison")
	}
	if !gresEqual("gpu:gpu.nvidia.com:8,nic:dranet0:1", "gpu:gpu.nvidia.com:8(S:0,2),nic:dranet0:1(S:1,3)") {
		t.Fatal("socket list suffix changed the GRES comparison")
	}
	if gresEqual("gpu:gpu.nvidia.com:8", "gpu:gpu.nvidia.com:4(S:0)") {
		t.Fatal("different GRES counts compared equal")
	}
}
