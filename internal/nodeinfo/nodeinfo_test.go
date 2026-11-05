// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

func TestSocketAllocation(t *testing.T) {
	ctx := context.Background()
	class := &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "cpus"}, Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: "device.driver == 'dra.cpu'"}}}}}
	client := fake.NewClientBuilder().WithObjects(class).Build()
	inventory := cpuSlice("node", socketDevice(3, "64-127", "0-127>32", true), socketDevice(0, "0-63", "0-127>32", true))
	n, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{inventory})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		cores      []int
		capacities []int64
		cpus       []string
	}{
		{"single socket", []int{0, 1, 2}, []int64{6}, []string{"0-2,32-34"}},
		{"uneven sockets", []int{0, 1, 2, 32, 33, 34, 35, 36}, []int64{6, 10}, []string{"0-2,32-34", "64-68,96-100"}},
		{"128 threads", func() []int {
			v := make([]int, 64)
			for i := range v {
				v[i] = i
			}
			return v
		}(), []int64{64, 64}, []string{"0-63", "64-127"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			resources := &slurmcontrol.NodeResources{CoreBitmap: bitmaputil.String(bitmaputil.New(tt.cores...)), SocketsPerNode: 2, CoresPerSocket: 32}
			got, err := n.AllocateCPUs(ctx, client, resources, "cpus", "pod-uid/job-17")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Requests) != len(tt.capacities) || len(got.Results) != len(tt.capacities) {
				t.Fatalf("wrong request/result counts: %+v", got)
			}
			total := int64(0)
			for i, capacity := range tt.capacities {
				req, result := got.Requests[i], got.Results[i]
				requested := req.Exactly.Capacity.Requests[nodeinfo.DraDriverCpu_Capacity]
				if req.Exactly.Count != 1 || requested.CmpInt64(capacity) != 0 {
					t.Fatalf("bad request %+v", req)
				}
				consumed := result.ConsumedCapacity[nodeinfo.DraDriverCpu_Capacity]
				if result.Request != req.Name || result.ShareID == nil || consumed.CmpInt64(capacity) != 0 {
					t.Fatalf("bad result %+v", result)
				}
				var config struct {
					CPUConfig struct {
						CPUSet string `json:"cpuset"`
					} `json:"cpuConfig"`
				}
				if err := json.Unmarshal(got.Config[i].Opaque.Parameters.Raw, &config); err != nil {
					t.Fatal(err)
				}
				if config.CPUConfig.CPUSet != tt.cpus[i] || !slices.Equal(got.Config[i].Requests, []string{req.Name}) {
					t.Fatalf("wrong scoped config %+v", got.Config[i])
				}
				total += capacity
			}
			if got.CPUCount != total {
				t.Fatalf("CPU capacity %d != %d", got.CPUCount, total)
			}
			retry, err := n.AllocateCPUs(ctx, client, resources, "cpus", "pod-uid/job-17")
			if err != nil {
				t.Fatal(err)
			}
			for i := range got.Results {
				if *retry.Results[i].ShareID != *got.Results[i].ShareID {
					t.Fatal("share ID changed on retry")
				}
			}
		})
	}
	for _, resources := range []*slurmcontrol.NodeResources{{CoreBitmap: bitmaputil.String(bitmaputil.New(0, 64))}, {CoreBitmap: ""}, {CoreBitmap: bitmaputil.String(bitmaputil.New(0)), SocketsPerNode: 1}} {
		if _, err := n.AllocateCPUs(ctx, client, resources, "cpus", "key"); err == nil {
			t.Fatalf("accepted invalid allocation %+v", resources)
		}
	}
	class.Spec.Selectors[0].CEL.Expression = "device.driver == 'dra.cpu' && device.attributes['dra.cpu'].socketID == 3"
	if err := client.Update(ctx, class); err != nil {
		t.Fatal(err)
	}
	if _, err := n.AllocateCPUs(ctx, client, &slurmcontrol.NodeResources{CoreBitmap: bitmaputil.String(bitmaputil.New(0))}, "cpus", "key"); err == nil {
		t.Fatal("ignored DeviceClass selector")
	}
}

func TestCPUFingerprint(t *testing.T) {
	slice := cpuSlice("node", socketDevice(0, "0-3", "0-7>2", true), socketDevice(1, "4-7", "0-7>2", true))
	n, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{slice})
	if err != nil {
		t.Fatal(err)
	}
	gres := `slurm-bridge.dra-gres-map={"v":1,"profiles":{"nic":{"firstIndex":0,"devices":["/dra/dra.net/node/eth0"]}}}`
	extra, err := n.EncodeApplied(gres)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := dra.AppliedCPUFingerprint(extra)
	if err != nil || !strings.HasPrefix(fingerprint, "v1:") || len(fingerprint) != 67 {
		t.Fatalf("invalid fingerprint %q: %v", fingerprint, err)
	}
	// Extra stores the GRES map and a fixed-size CPU fingerprint.
	if extra != strings.TrimSuffix(gres, "}")+`,"cpuFingerprint":"`+fingerprint+`"}` {
		t.Fatalf("unexpected persisted CPU data: %s", extra)
	}
	for _, tc := range []struct {
		name      string
		change    func(*resourcev1.ResourceSlice)
		wantMatch bool
	}{
		{"restart", func(*resourcev1.ResourceSlice) {}, true},
		{"publication generation", func(s *resourcev1.ResourceSlice) { s.Spec.Pool.Generation++ }, true},
		{"device ordering", func(s *resourcev1.ResourceSlice) { slices.Reverse(s.Spec.Devices) }, true},
		{"equivalent CPU and SMT encoding", func(s *resourcev1.ResourceSlice) {
			s.Spec.Devices[0].Attributes[nodeinfo.DraDriverCpu_CPUIDs] = resourcev1.DeviceAttribute{StringValue: ptr.To("3,1,0,2")}
			s.Spec.Devices[0].Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("0-3>2;4-7>2")}
		}, true},
		{"same geometry different CPUs", func(s *resourcev1.ResourceSlice) {
			s.Spec.Devices[0] = socketDevice(0, "8-11", "8-15>2", true)
			s.Spec.Devices[1] = socketDevice(1, "12-15", "8-15>2", true)
		}, false},
		{"different siblings", func(s *resourcev1.ResourceSlice) {
			for i := range s.Spec.Devices {
				s.Spec.Devices[i].Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("0-7>1")}
			}
		}, false},
		{"pool identity", func(s *resourcev1.ResourceSlice) { s.Spec.Pool.Name = "replacement" }, false},
		{"device identity", func(s *resourcev1.ResourceSlice) { s.Spec.Devices[0].Name = "replacement" }, false},
		{"selection attributes", func(s *resourcev1.ResourceSlice) {
			s.Spec.Devices[0].Attributes["dra.cpu/model"] = resourcev1.DeviceAttribute{StringValue: ptr.To("replacement")}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := slice.DeepCopy()
			tc.change(current)
			rebuilt, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{*current})
			if err != nil {
				t.Fatal(err)
			}
			if err := rebuilt.ValidateCPUFingerprint(extra); (err == nil) != tc.wantMatch {
				t.Fatalf("want match=%v, got %v", tc.wantMatch, err)
			}
		})
	}
	for _, invalid := range []string{"", gres, "corrupt", strings.Replace(extra, "v1:", "v99:", 1), strings.Replace(extra, fingerprint, "invalid", 1), `slurm-bridge.dra-gres-map={"v":2,"profiles":{},"cpu":{"v":1}}`} {
		if err := n.ValidateCPUFingerprint(invalid); err == nil {
			t.Fatalf("accepted missing/invalid fingerprint: %s", invalid)
		}
	}
	slices.Reverse(n.CpuMap.AbstractToMachine)
	if err := n.ValidateCPUFingerprint(extra); err == nil {
		t.Fatal("changed abstract core order matched registration")
	}
	if err := (&nodeinfo.NodeInfo{}).ValidateCPUFingerprint(extra); err == nil {
		t.Fatal("missing live inventory matched registration")
	}
}

func TestCompleteCPUPool(t *testing.T) {
	a := cpuSlice("node", socketDevice(0, "0-1", "0-1", false))
	a.Spec.Pool.ResourceSliceCount = 2
	b := *a.DeepCopy()
	b.Name = "node-cpu-b"
	b.Spec.Devices = []resourcev1.Device{socketDevice(1, "2-3", "2-3", false)}
	if _, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{b, a}); err != nil {
		t.Fatal(err)
	}
	for i, slices := range [][]resourcev1.ResourceSlice{
		{a}, {a, a}, {a, func() resourcev1.ResourceSlice { x := *b.DeepCopy(); x.Spec.Pool.Generation++; return x }()},
		{a, func() resourcev1.ResourceSlice { x := *b.DeepCopy(); x.Spec.Pool.ResourceSliceCount = 1; return x }()},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := nodeinfo.NewNodeInfoFromResourceSlices("node", slices); err == nil {
				t.Fatal("accepted incomplete/inconsistent pool")
			}
		})
	}
	old := *a.DeepCopy()
	old.Spec.Pool.Generation--
	old.Name = "old"
	if _, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{old, b, a}); err != nil {
		t.Fatal(err)
	}
	b.Spec.NodeName = ptr.To("another-node")
	if _, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{a, b}); err == nil {
		t.Fatal("used a different node's slice")
	}
}
