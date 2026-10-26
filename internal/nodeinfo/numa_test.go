// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

func numaDevice(socket, numa int64, cpus, smt string) resourcev1.Device {
	device := socketDevice(socket, cpus, smt, false)
	device.Name = fmt.Sprintf("numa-%d", numa)
	device.Attributes[nodeinfo.DraDriverCpu_NUMANodeID] = resourcev1.DeviceAttribute{IntValue: ptr.To(numa)}
	return device
}

func TestNUMATopology(t *testing.T) {
	// Two sockets with two NUMA nodes each, as with sub-NUMA clustering.
	slice := cpuSlice("node",
		numaDevice(1, 3, "6-7", "0-7"), numaDevice(0, 0, "0-1", "0-7"),
		numaDevice(1, 2, "4-5", "0-7"), numaDevice(0, 1, "2-3", "0-7"))
	n, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{slice})
	if err != nil {
		t.Fatal(err)
	}
	sockets, cores, threads, err := n.Geometry()
	if err != nil || sockets != 2 || cores != 4 || threads != 1 {
		t.Fatalf("Geometry() = %d/%d/%d, %v; want 2/4/1", sockets, cores, threads, err)
	}
	// Slurm binds GRES only to whole sockets.
	if got := n.NUMACores(); got != nil {
		t.Fatalf("NUMACores() with sub-NUMA clustering = %v, want nil", got)
	}

	ctx := context.Background()
	class := &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "cpus"}, Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'dra.cpu'"}}}}}
	client := fake.NewClientBuilder().WithObjects(class).Build()
	resources := &slurmcontrol.NodeResources{CoreBitmap: bitmaputil.String(bitmaputil.New(1, 2, 3, 5)), SocketsPerNode: 2, CoresPerSocket: 4}
	got, err := n.AllocateCPUs(ctx, client, resources, "cpus", "pod-uid/job-1")
	if err != nil {
		t.Fatal(err)
	}
	wantCPUs := []string{"1", "2-3", "5"}
	if len(got.Requests) != len(wantCPUs) || got.CPUCount != 4 {
		t.Fatalf("AllocateCPUs() = %+v, want %d requests for 4 CPUs", got, len(wantCPUs))
	}
	for i, cpus := range wantCPUs {
		var config struct {
			CPUConfig struct {
				CPUSet string `json:"cpuset"`
			} `json:"cpuConfig"`
		}
		if err := json.Unmarshal(got.Config[i].Opaque.Parameters.Raw, &config); err != nil {
			t.Fatal(err)
		}
		selector := fmt.Sprintf("device.driver == 'dra.cpu' && device.attributes['dra.cpu'].numaNodeID == %d", i)
		if got.Requests[i].Name != fmt.Sprintf("cpu-numa-%d", i) || got.Requests[i].Exactly.Selectors[0].CEL.Expression != selector ||
			got.Results[i].Device != fmt.Sprintf("numa-%d", i) || config.CPUConfig.CPUSet != cpus {
			t.Fatalf("allocation %d = %+v %+v %s, want NUMA node %d with CPUs %s", i, got.Requests[i], got.Results[i], config.CPUConfig.CPUSet, i, cpus)
		}
	}
}

func TestNUMACores(t *testing.T) {
	slice := cpuSlice("node", numaDevice(1, 1, "4-7", "0-7"), numaDevice(0, 0, "0-3", "0-7"))
	n, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{slice})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := n.NUMACores(), map[int][]int{0: {0, 1, 2, 3}, 1: {4, 5, 6, 7}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NUMACores() = %v, want %v", got, want)
	}
}

func TestSocketTopologyHasNoNUMACores(t *testing.T) {
	n, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{cpuSlice("node", socketDevice(0, "0-3", "0-3", false))})
	if err != nil {
		t.Fatal(err)
	}
	if got := n.NUMACores(); got != nil {
		t.Fatalf("NUMACores() = %v, want nil", got)
	}
	if got := (*nodeinfo.NodeInfo)(nil).NUMACores(); got != nil {
		t.Fatalf("nil NUMACores() = %v, want nil", got)
	}
}

func TestInvalidNUMATopology(t *testing.T) {
	for name, devices := range map[string][]resourcev1.Device{
		"mixed grouping":     {numaDevice(0, 0, "0-1", "0-3"), socketDevice(1, "2-3", "0-3", false)},
		"NUMA spans sockets": {numaDevice(0, 0, "0-1", "0-3"), func() resourcev1.Device { d := numaDevice(1, 0, "2-3", "0-3"); d.Name = "other"; return d }()},
		"invalid NUMA node": {func() resourcev1.Device {
			d := numaDevice(0, 0, "0-1", "0-1")
			d.Attributes[nodeinfo.DraDriverCpu_NUMANodeID] = resourcev1.DeviceAttribute{StringValue: ptr.To("0")}
			return d
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{cpuSlice("node", devices...)}); err == nil {
				t.Fatal("accepted invalid NUMA topology")
			}
		})
	}
}
