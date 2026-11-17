// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

type topologyInformer struct {
	cache.ResourceEventHandlerRegistration
	synced bool
}

func (f *topologyInformer) AddEventHandler(cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return f, nil
}
func (f *topologyInformer) HasSynced() bool { return f.synced }

func topologySlice(node string) *resourcev1.ResourceSlice {
	return &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: node, UID: types.UID(node)}, Spec: resourcev1.ResourceSliceSpec{
		Driver: DraDriverCpu, NodeName: ptr.To(node), Pool: resourcev1.ResourcePool{Name: node, Generation: 1, ResourceSliceCount: 1},
		Devices: []resourcev1.Device{{Name: "socket-0", AllowMultipleAllocations: ptr.To(true), Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			DraDriverCpu_SocketID: {IntValue: ptr.To(int64(0))}, DraDriverCpu_CPUIDs: {StringValue: ptr.To("0-127")}, DraDriverCpu_SMTLayout: {StringValue: ptr.To("0-127>64")}, DraDriverCpu_SMTEnabled: {BoolValue: ptr.To(true)},
		}, Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{DraDriverCpu_Capacity: {Value: resource.MustParse("128")}}}},
	}}
}

func TestCPUTopologyCacheInvalidation(t *testing.T) {
	f := &topologyInformer{}
	c, err := NewCPUTopologyCache(f)
	if err != nil {
		t.Fatal(err)
	}
	a, b := topologySlice("a"), topologySlice("b")
	before := a.DeepCopy()
	c.OnAdd(a, true)
	c.OnAdd(b, true)
	if _, err := c.topology("a"); err == nil {
		t.Fatal("used unsynchronized inventory")
	}
	f.synced = true
	first, err := c.topology("a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.topology("b")
	if err != nil {
		t.Fatal(err)
	}
	if next, _ := c.topology("a"); next != first {
		t.Fatal("rebuilt unchanged topology")
	}
	// Publishing part of a newer generation must reject, never use generation 1.
	newer := a.DeepCopy()
	newer.Name = "a-new"
	newer.UID = "new"
	newer.Spec.Pool.Generation++
	newer.Spec.Pool.ResourceSliceCount = 2
	c.OnAdd(newer, false)
	if _, err := c.topology("a"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("got %v, want incomplete generation", err)
	}
	if next, _ := c.topology("b"); next != other {
		t.Fatal("invalidated unrelated node")
	}
	complete := newer.DeepCopy()
	complete.Spec.Pool.ResourceSliceCount = 1
	c.OnUpdate(newer, complete)
	updated, err := c.topology("a")
	if err != nil || updated == first {
		t.Fatalf("new generation not rebuilt: %v", err)
	}
	// Replacement and stale tombstones must not delete the replacement.
	replacement := complete.DeepCopy()
	replacement.UID = "replacement"
	c.OnUpdate(complete, replacement)
	replaced, err := c.topology("a")
	if err != nil {
		t.Fatal(err)
	}
	c.OnDelete(cache.DeletedFinalStateUnknown{Obj: complete})
	if next, _ := c.topology("a"); next != replaced {
		t.Fatal("stale tombstone invalidated replacement")
	}
	// Unsupported global selection must fail even when a node has cached topology.
	global := topologySlice("global")
	global.Spec.NodeName = nil
	global.Spec.AllNodes = ptr.To(true)
	c.OnAdd(global, false)
	if _, err := c.topology("b"); err == nil {
		t.Fatal("accepted global CPU inventory")
	}
	c.OnDelete(global)
	if _, err := c.topology("b"); err != nil {
		t.Fatal(err)
	}
	// Moving a slice invalidates both nodes, including removal from the old node.
	moved := b.DeepCopy()
	moved.Spec.NodeName = ptr.To("c")
	c.OnUpdate(b, moved)
	old, err := c.topology("b")
	if err != nil || len(old.CPUDevices) != 0 {
		t.Fatal("retained moved inventory")
	}
	if n, err := c.topology("c"); err != nil || len(n.CPUDevices) == 0 {
		t.Fatal("missing moved inventory")
	}
	c.OnDelete(moved)
	if n, err := c.topology("c"); err != nil || len(n.CPUDevices) != 0 {
		t.Fatal("retained deleted inventory")
	}
	if !reflect.DeepEqual(a, before) {
		t.Fatal("mutated informer object")
	}
}

func TestCPUTopologyCacheAllocationIsolation(t *testing.T) {
	c, _ := NewCPUTopologyCache(&topologyInformer{synced: true})
	s := topologySlice("node")
	c.OnAdd(s, true)
	n, err := c.topology("node")
	if err != nil {
		t.Fatal(err)
	}
	extra, err := n.EncodeApplied("")
	if err != nil {
		t.Fatal(err)
	}
	r := &slurmcontrol.NodeResources{NodeExtra: extra, CoreBitmap: bitmaputil.String(bitmaputil.New(0)), SocketsPerNode: 1, CoresPerSocket: 64}
	class := &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "cpus"}}
	reader := fake.NewClientBuilder().WithObjects(class).Build()
	ctx := context.Background()
	a, err := c.AllocateCPUs(ctx, reader, "node", r, "cpus", "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	want, err := n.AllocateCPUs(ctx, reader, r, "cpus", "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, want) {
		t.Fatal("cached topology changed allocation")
	}
	a.Requests[0].Name = "mutated"
	a.Config[0].Opaque.Parameters.Raw[0] = 'x'
	b, err := c.AllocateCPUs(ctx, reader, "node", r, "cpus", "pod-b")
	if err != nil {
		t.Fatal(err)
	}
	if b.Requests[0].Name == "mutated" || b.Config[0].Opaque.Parameters.Raw[0] == 'x' || *a.Results[0].ShareID == *b.Results[0].ShareID {
		t.Fatal("shared mutable allocation or share ID")
	}
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Go(func() {
			for range 10 {
				if _, err := c.AllocateCPUs(ctx, reader, "node", r, "cpus", fmt.Sprint(worker)); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Wait()
	newer := s.DeepCopy()
	newer.Spec.Pool.Generation++
	newer.Spec.Devices[0].Attributes[DraDriverCpu_CPUIDs] = resourcev1.DeviceAttribute{StringValue: ptr.To("128-255")}
	newer.Spec.Devices[0].Attributes[DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("128-255>64")}
	c.OnUpdate(s, newer)
	if _, err := c.AllocateCPUs(ctx, reader, "node", r, "cpus", "pod-c"); err == nil {
		t.Fatal("accepted obsolete applied fingerprint")
	}
}

func TestCPUTopologyCacheConcurrentUpdates(t *testing.T) {
	c, _ := NewCPUTopologyCache(&topologyInformer{synced: true})
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for i := range 50 {
				s := topologySlice(fmt.Sprint(worker))
				s.ResourceVersion = fmt.Sprint(i)
				c.OnAdd(s, false)
				if _, err := c.topology(fmt.Sprint(worker)); err != nil {
					t.Error(err)
				}
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 100 {
				_, _ = c.topology("0")
			}
		})
	}
	wg.Wait()
}

func BenchmarkCPUTopology(b *testing.B) {
	s := topologySlice("node")
	b.Run("rebuild", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{*s}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		c, _ := NewCPUTopologyCache(&topologyInformer{synced: true})
		c.OnAdd(s, true)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.topology("node"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
