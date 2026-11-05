// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"fmt"
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
	for _, subNUMA := range []bool{false, true} {
		t.Run(fmt.Sprintf("subNUMA=%v", subNUMA), func(t *testing.T) {
			testNUMARegistrationBindsGRESToCores(t, subNUMA)
		})
	}
}

func testNUMARegistrationBindsGRESToCores(t *testing.T, subNUMA bool) {
	slice := testNodeNUMAResourceSlice("worker")
	if subNUMA {
		for i := range slice.Spec.Devices {
			slice.Spec.Devices[i].Attributes[nodeinfo.DraDriverCpu_SocketID] = resourcev1.DeviceAttribute{IntValue: ptr.To[int64](0)}
		}
	}
	info := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*slice})
	geometry, err := desiredNodeCPUConfig(&corev1.Node{}, info)
	if err != nil || geometry.sockets != 2 || geometry.coresPerSocket != 2 || geometry.threadsPerCore != 1 {
		t.Fatalf("CPU registration = %+v, %v; want 2/2/1", geometry, err)
	}
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

func TestNUMARegistrationTwoSocketsSubNUMA(t *testing.T) {
	// Two sockets with two NUMA domains each (SNC2): every domain is a Slurm socket.
	slice := testNodeCPUResourceSlice("worker")
	device := func(socket, numa int64, cpus string) resourcev1.Device {
		return resourcev1.Device{Name: fmt.Sprintf("cpudevnuma%d", numa), AllowMultipleAllocations: ptr.To(true), Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To(socket)}, nodeinfo.DraDriverCpu_NUMANodeID: {IntValue: ptr.To(numa)},
			nodeinfo.DraDriverCpu_SMTEnabled: {BoolValue: ptr.To(false)}, nodeinfo.DraDriverCpu_CPUIDs: {StringValue: ptr.To(cpus)},
			nodeinfo.DraDriverCpu_SMTLayout: {StringValue: ptr.To("0-7")},
		}, Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{nodeinfo.DraDriverCpu_Capacity: {Value: resource.MustParse("2")}}}
	}
	slice.Spec.Devices = []resourcev1.Device{device(1, 3, "6-7"), device(0, 0, "0-1"), device(1, 2, "4-5"), device(0, 1, "2-3")}
	info := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*slice})
	geometry, err := desiredNodeCPUConfig(&corev1.Node{}, info)
	if err != nil || geometry.sockets != 4 || geometry.coresPerSocket != 2 || geometry.threadsPerCore != 1 {
		t.Fatalf("CPU registration = %+v, %v; want 4/2/1", geometry, err)
	}
	gpus := make([]dra.DeviceIdentity, 4)
	numa := make(map[dra.DeviceIdentity][]int, 4)
	for i := range gpus {
		gpus[i] = structured.MakeDeviceID("gpu.example.com", "pool-a", fmt.Sprintf("gpu-%d", i))
		numa[gpus[i]] = []int{i}
	}
	// A device local to both domains of socket 1 binds to both of its Slurm sockets.
	nic := structured.MakeDeviceID("nic.example.com", "pool-a", "nic-0")
	inventory := []dra.GRESInventory{
		{GRES: dra.GRES{Name: "gpu", Type: "gpu.example.com"}, Devices: gpus, NUMANodes: numa},
		{GRES: dra.GRES{Name: "nic", Type: "nic.example.com"}, Devices: []dra.DeviceIdentity{nic}, NUMANodes: map[dra.DeviceIdentity][]int{nic: {2, 3}}},
	}
	got, err := buildNodeGRESConfig(inventory, info)
	if err != nil {
		t.Fatal(err)
	}
	want := "count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-0,cpus=0-1+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-1,cpus=2-3+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-2,cpus=4-5+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-3,cpus=6-7+" +
		"count=1,name=nic,type=nic.example.com,file=/dra/nic.example.com/pool-a/nic-0,cpus=4-7"
	if got.gresConf != want {
		t.Fatalf("GresConf = %q,\nwant %q", got.gresConf, want)
	}
}
