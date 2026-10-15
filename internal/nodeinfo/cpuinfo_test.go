// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"fmt"
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
)

func socketDevice(socket int64, cpus, smt string, smtEnabled bool) resourcev1.Device {
	set, err := cpuset.Parse(cpus)
	if err != nil {
		panic(err)
	}
	return resourcev1.Device{Name: fmt.Sprintf("socket-%d", socket), AllowMultipleAllocations: ptr.To(true),
		Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			nodeinfo.DraDriverCpu_SocketID:   {IntValue: ptr.To(socket)},
			nodeinfo.DraDriverCpu_CPUIDs:     {StringValue: ptr.To(cpus)},
			nodeinfo.DraDriverCpu_SMTLayout:  {StringValue: ptr.To(smt)},
			nodeinfo.DraDriverCpu_SMTEnabled: {BoolValue: ptr.To(smtEnabled)},
		},
		Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{nodeinfo.DraDriverCpu_Capacity: {Value: *resource.NewQuantity(int64(set.Size()), resource.DecimalSI)}},
	}
}

func cpuSlice(node string, devices ...resourcev1.Device) resourcev1.ResourceSlice {
	return resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: node + "-cpu"}, Spec: resourcev1.ResourceSliceSpec{
		NodeName: ptr.To(node), Driver: nodeinfo.DraDriverCpu,
		Pool: resourcev1.ResourcePool{Name: node, Generation: 7, ResourceSliceCount: 1}, Devices: devices,
	}}
}

func TestSocketTopology(t *testing.T) {
	for _, tt := range []struct {
		name, cpus, smt string
		smtEnabled      bool
		cores           []string
	}{
		{"SMT", "0-3,8-11", "0-3,8-11>8", true, []string{"0,8", "1,9", "2,10", "3,11"}},
		{"no SMT", "2,7,19", "2,7,19", false, []string{"2", "7", "19"}},
		{"reserved whole core", "1-3,9-11", "0-3,8-11>8", true, []string{"1,9", "2,10", "3,11"}},
		{"reserved SMT siblings", "0-3", "0-3,8-11>8", true, []string{"0", "1", "2", "3"}},
		{"reserved first siblings", "8-11", "0-3,8-11>8", true, []string{"8", "9", "10", "11"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{cpuSlice("node", socketDevice(4, tt.cpus, tt.smt, tt.smtEnabled))})
			if err != nil {
				t.Fatal(err)
			}
			if len(n.CpuMap.AbstractToMachine) != len(tt.cores) {
				t.Fatalf("cores: %v", n.CpuMap.AbstractToMachine)
			}
			for i, want := range tt.cores {
				if got := n.CpuMap.AbstractToMachine[i].String(); got != want {
					t.Fatalf("core %d: %s, want %s", i, got, want)
				}
			}
		})
	}
}

func TestRejectMalformedSocketTopology(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*resourcev1.Device)
		want   string
	}{
		{"individual", func(d *resourcev1.Device) { delete(d.Attributes, nodeinfo.DraDriverCpu_CPUIDs) }, "cpuIDs"},
		{"SMT missing", func(d *resourcev1.Device) { delete(d.Attributes, nodeinfo.DraDriverCpu_SMTLayout) }, "smtLayout"},
		{"obsolete SMT map", func(d *resourcev1.Device) {
			d.Attributes["dra.cpu/smtMap"] = d.Attributes[nodeinfo.DraDriverCpu_SMTLayout]
			delete(d.Attributes, nodeinfo.DraDriverCpu_SMTLayout)
		}, "smtLayout"},
		{"unsupported SMT map version", func(d *resourcev1.Device) {
			d.Attributes["dra.cpu/smtLayoutV2"] = d.Attributes[nodeinfo.DraDriverCpu_SMTLayout]
			delete(d.Attributes, nodeinfo.DraDriverCpu_SMTLayout)
		}, "smtLayout"},
		{"obsolete versioned SMT map", func(d *resourcev1.Device) {
			d.Attributes["dra.cpu/smtMapV1"] = d.Attributes[nodeinfo.DraDriverCpu_SMTLayout]
			delete(d.Attributes, nodeinfo.DraDriverCpu_SMTLayout)
		}, "smtLayout"},
		{"SMT flag missing", func(d *resourcev1.Device) {
			delete(d.Attributes, nodeinfo.DraDriverCpu_SMTEnabled)
		}, "must be a boolean"},
		{"SMT flag wrong type", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_SMTEnabled] = resourcev1.DeviceAttribute{IntValue: ptr.To[int64](2)}
		}, "must be a boolean"},
		{"disabled SMT with siblings", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_SMTEnabled] = resourcev1.DeviceAttribute{BoolValue: ptr.To(false)}
		}, "contradicts disabled SMT"},
		{"wrong capacity", func(d *resourcev1.Device) {
			d.Capacity[nodeinfo.DraDriverCpu_Capacity] = resourcev1.DeviceCapacity{Value: resource.MustParse("3")}
		}, "capacity"},
		{"no sharing", func(d *resourcev1.Device) { d.AllowMultipleAllocations = nil }, "multiple allocations"},
		{"corrupt SMT", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("-3>2")}
		}, "invalid CPU"},
		{"conflicting SMT", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("0-3>2;0")}
		}, "duplicate CPU"},
		{"missing sibling", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("0-2>2")}
		}, "no sibling"},
		{"SMT coverage", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("0,2>2")}
		}, "missing"},
		{"zero stride", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("0-3>0")}
		}, "invalid SMT stride"},
		{"oversized set", func(d *resourcev1.Device) {
			d.Attributes[nodeinfo.DraDriverCpu_CPUIDs] = resourcev1.DeviceAttribute{StringValue: ptr.To("0-2147483647")}
		}, "exceeds supported"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := socketDevice(0, "0-3", "0-3>2", true)
			tt.mutate(&d)
			s := cpuSlice("node", d)
			_, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{s})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
}

func TestRejectNonRectangularAndOverlappingTopology(t *testing.T) {
	cases := [][]resourcev1.Device{
		{socketDevice(0, "0-2", "0-3>2", true)},
		{socketDevice(0, "0-1", "0-1", false), socketDevice(1, "2-4", "2-4", false)},
		{socketDevice(0, "0-1", "0-1", false), socketDevice(1, "1-2", "1-2", false)},
		{socketDevice(0, "0-1", "0-1", false), socketDevice(0, "2-3", "2-3", false)},
		{socketDevice(0, "0", "0-1>1", true), socketDevice(1, "1", "0-1>1", true)},
	}
	for i, devices := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{cpuSlice("node", devices...)})
			if err == nil {
				t.Fatal("accepted unsafe topology")
			}
		})
	}
}
