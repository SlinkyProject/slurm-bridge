// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"
)

func TestDeviceNUMANodes(t *testing.T) {
	tests := []struct {
		name      string
		attribute *resourcev1.DeviceAttribute
		want      []int
	}{
		{name: "missing"},
		{name: "scalar", attribute: &resourcev1.DeviceAttribute{IntValue: ptr.To[int64](1)}, want: []int{1}},
		{name: "list", attribute: &resourcev1.DeviceAttribute{IntValues: []int64{0, 1}}, want: []int{0, 1}},
		{name: "unordered list", attribute: &resourcev1.DeviceAttribute{IntValues: []int64{3, 1, 3}}, want: []int{1, 3}},
		{name: "negative", attribute: &resourcev1.DeviceAttribute{IntValue: ptr.To[int64](-1)}},
		{name: "string", attribute: &resourcev1.DeviceAttribute{StringValue: ptr.To("0")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &resourcev1.Device{Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{}}
			if tt.attribute != nil {
				device.Attributes[deviceattribute.StandardDeviceAttributeNUMANode] = *tt.attribute
			}
			if got := deviceNUMANodes(device); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("deviceNUMANodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildNodeInventoryNUMANodes(t *testing.T) {
	numa := func(values ...int64) map[resourcev1.QualifiedName]resourcev1.DeviceAttribute {
		return map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			deviceattribute.StandardDeviceAttributeNUMANode: {IntValues: values},
		}
	}
	slice := resourcev1.ResourceSlice{Spec: resourcev1.ResourceSliceSpec{
		NodeName: ptr.To("node-a"),
		Driver:   "gpu.example.com",
		Pool:     resourcev1.ResourcePool{Name: "pool", Generation: 1, ResourceSliceCount: 1},
		Devices: []resourcev1.Device{
			{Name: "gpu-0", Attributes: numa(0)},
			{Name: "gpu-1", Attributes: numa(1, 0)},
			{Name: "gpu-2"},
		},
	}}

	got, err := BuildNodeInventory(context.Background(), registryWithExampleGPU(), nodeForTest("node-a"), []resourcev1.ResourceSlice{slice})
	if err != nil {
		t.Fatalf("BuildNodeInventory() error = %v", err)
	}
	want := map[DeviceIdentity][]int{
		deviceIDForTest("gpu.example.com", "pool", "gpu-0"): {0},
		deviceIDForTest("gpu.example.com", "pool", "gpu-1"): {0, 1},
	}
	if len(got.Profiles) != 1 || !reflect.DeepEqual(got.Profiles[0].NUMANodes, want) {
		t.Fatalf("BuildNodeInventory() NUMA nodes = %#v, want %#v", got.Profiles, want)
	}
}

func TestGRESInventoryNUMACores(t *testing.T) {
	gpu0 := deviceIDForTest("gpu.example.com", "pool", "gpu-0")
	gpu1 := deviceIDForTest("gpu.example.com", "pool", "gpu-1")
	gpu2 := deviceIDForTest("gpu.example.com", "pool", "gpu-2")
	gpu3 := deviceIDForTest("gpu.example.com", "pool", "gpu-3")
	gpu4 := deviceIDForTest("gpu.example.com", "pool", "gpu-4")
	inventory := GRESInventory{
		GRES:    GRES{Name: "gpu", Type: "gpu.example.com"},
		Devices: []DeviceIdentity{gpu0, gpu1, gpu2, gpu3, gpu4},
		NUMANodes: map[DeviceIdentity][]int{
			gpu0: {0},
			gpu1: {0, 1},
			gpu2: {0, 2}, // not one core range
			gpu3: {3},    // no CPUs
		},
	}.WithNUMACores(map[int][]int{0: {0, 1, 2, 3}, 1: {4, 5, 6, 7}, 2: {8, 9, 10, 11}})

	_, gresConf, err := inventory.SlurmConfig()
	if err != nil {
		t.Fatalf("GRESInventory.SlurmConfig() error = %v", err)
	}
	wantConf := "count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool/gpu-0,cpus=0-3+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool/gpu-1,cpus=0-7+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool/gpu-2+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool/gpu-3+" +
		"count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool/gpu-4"
	if gresConf != wantConf {
		t.Fatalf("GRESInventory.SlurmConfig() GresConf = %q, want %q", gresConf, wantConf)
	}

	extra, err := EncodeAppliedInventory([]GRESInventory{inventory})
	if err != nil {
		t.Fatalf("EncodeAppliedInventory() error = %v", err)
	}
	if want := `"cores":["0-3","0-7","","",""]`; !strings.Contains(extra, want) {
		t.Fatalf("EncodeAppliedInventory() = %q, want it to contain %s", extra, want)
	}
	if _, err := DecodeAppliedInventory(extra); err != nil {
		t.Fatalf("DecodeAppliedInventory() error = %v", err)
	}
	if cores := inventory.WithNUMACores(nil).Cores; cores != nil {
		t.Fatalf("WithNUMACores(nil) cores = %v, want none", cores)
	}
}

func TestNUMASpans(t *testing.T) {
	registry, err := NewRegistry([]DeviceProfile{
		{Name: "gpu.example.com", Driver: "gpu.example.com", Selector: `device.driver == 'gpu.example.com'`, Backend: IndexedGRESBackend{GRESName: "gpu"}},
		{Name: "nic.example.com", Driver: "nic.example.com", Selector: `device.driver == 'nic.example.com'`, Backend: IndexedGRESBackend{GRESName: "nic"}},
		{Name: "fpga.example.com", Driver: "fpga.example.com", Selector: `device.driver == 'fpga.example.com'`, Backend: IndexedGRESBackend{GRESName: "fpga"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	slice := func(node, driver string, numaNodes ...int64) *resourcev1.ResourceSlice {
		s := &resourcev1.ResourceSlice{Spec: resourcev1.ResourceSliceSpec{
			NodeName: ptr.To(node), Driver: driver,
			Pool: resourcev1.ResourcePool{Name: node, Generation: 1, ResourceSliceCount: 1},
		}}
		for i, numa := range numaNodes {
			device := resourcev1.Device{Name: fmt.Sprintf("dev-%d", i)}
			if numa >= 0 {
				device.Attributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
					deviceattribute.StandardDeviceAttributeNUMANode: {IntValues: []int64{numa}},
				}
			}
			s.Spec.Devices = append(s.Spec.Devices, device)
		}
		return s
	}
	resourceSlices := []*resourcev1.ResourceSlice{
		slice("node-a", "gpu.example.com", 0, 0, 1, 1),
		slice("node-a", "nic.example.com", 1),
		slice("node-b", "gpu.example.com", 0, 1, -1),
		slice("node-b", "nic.example.com", 0, 1),
		slice("node-b", "fpga.example.com", -1),
		slice("node-b", "unknown.example.com", 0),
	}

	got := NUMASpans(context.Background(), registry, resourceSlices, []string{"gpu.example.com", "nic.example.com", "fpga.example.com"})
	want := map[string]float64{"gpu.example.com": 2, "nic.example.com": 1.5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NUMASpans() = %v, want %v", got, want)
	}
	if got := NUMASpans(context.Background(), registry, resourceSlices, []string{"nic.example.com"}); !reflect.DeepEqual(got, map[string]float64{"nic.example.com": 1.5}) {
		t.Fatalf("NUMASpans() for one profile = %v", got)
	}
}
