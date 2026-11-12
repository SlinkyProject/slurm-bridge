// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"
)

type spanInformer struct {
	cache.ResourceEventHandlerRegistration
	handler cache.ResourceEventHandler
	synced  bool
}

func (f *spanInformer) AddEventHandler(h cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	f.handler = h
	return f, nil
}
func (f *spanInformer) HasSynced() bool { return f.synced }

func spanSlice(name, node string, numa ...int64) *resourcev1.ResourceSlice {
	s := &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name)}, Spec: resourcev1.ResourceSliceSpec{
		NodeName: ptr.To(node), Driver: "gpu.example.com", Pool: resourcev1.ResourcePool{Name: node, Generation: 1, ResourceSliceCount: 1},
	}}
	for i, n := range numa {
		s.Spec.Devices = append(s.Spec.Devices, resourcev1.Device{Name: fmt.Sprintf("gpu-%d", i), Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			deviceattribute.StandardDeviceAttributeNUMANode: {IntValues: []int64{n}},
		}})
	}
	return s
}
func spanRegistry(t testing.TB) *Registry {
	t.Helper()
	r, err := NewRegistry([]DeviceProfile{{Name: "gpu", Driver: "gpu.example.com", Selector: `device.driver == 'gpu.example.com'`, Backend: IndexedGRESBackend{GRESName: "gpu"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestNUMASpanCacheLifecycle(t *testing.T) {
	f := &spanInformer{}
	c, err := NewNUMASpanCache(spanRegistry(t), f)
	if err != nil {
		t.Fatal(err)
	}
	a, b := spanSlice("a", "node-a", 0), spanSlice("b", "node-b", 0, 1)
	before := a.DeepCopy()
	f.handler.OnAdd(a, true)
	f.handler.OnAdd(b, true)
	if got := c.Spans(context.Background(), []string{"gpu"}); got != nil {
		t.Fatalf("unsynced result: %v", got)
	}
	f.synced = true
	check := func(want float64) {
		t.Helper()
		if got := c.Spans(context.Background(), []string{"gpu"})["gpu"]; got != want {
			t.Fatalf("span=%v want %v", got, want)
		}
	}
	check(1.5)
	c.Spans(context.Background(), []string{"gpu"})["gpu"] = 99
	check(1.5)
	updated := b.DeepCopy()
	updated.Spec.Devices = updated.Spec.Devices[:1]
	f.handler.OnUpdate(b, updated)
	check(1)
	replacement := spanSlice("a", "node-a", 0, 1, 2)
	replacement.UID = "replacement"
	f.handler.OnUpdate(a, replacement)
	check(2)
	f.handler.OnDelete(cache.DeletedFinalStateUnknown{Key: "a", Obj: a})
	check(2)
	f.handler.OnDelete(cache.DeletedFinalStateUnknown{Key: "a", Obj: replacement})
	check(1)
	f.handler.OnDelete(updated)
	if got := c.Spans(context.Background(), []string{"gpu"}); len(got) != 0 {
		t.Fatalf("deleted inventory retained: %v", got)
	}
	if !reflect.DeepEqual(a, before) {
		t.Fatal("mutated informer object")
	}
	// A canceled computation must not poison the cached result.
	f.handler.OnAdd(a, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Spans(ctx, []string{"gpu"})
	check(1)
}
func TestNUMASpanCacheConcurrentUpdates(t *testing.T) {
	f := &spanInformer{synced: true}
	c, err := NewNUMASpanCache(spanRegistry(t), f)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range 100 {
				s := spanSlice(fmt.Sprint(i%3), "node", 0, 1)
				c.OnAdd(s, false)
				c.Spans(context.Background(), []string{"gpu"})
				c.OnDelete(s)
			}
		})
	}
	wg.Wait()
}
func BenchmarkNUMASpanCache(b *testing.B) {
	for _, nodes := range []int{64, 640} {
		b.Run(fmt.Sprint(nodes), func(b *testing.B) {
			f := &spanInformer{synced: true}
			r := spanRegistry(b)
			c, err := NewNUMASpanCache(r, f)
			if err != nil {
				b.Fatal(err)
			}
			slices := make([]*resourcev1.ResourceSlice, nodes)
			for i := range nodes {
				slices[i] = spanSlice(fmt.Sprint(i), fmt.Sprint(i), 0, 1, 2, 3, 0, 1, 2, 3)
				c.OnAdd(slices[i], true)
			}
			profiles := []string{"gpu"}
			c.Spans(context.Background(), profiles)
			b.Run("scan", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					NUMASpans(context.Background(), r, slices, profiles)
				}
			})
			b.Run("cached", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					c.Spans(context.Background(), profiles)
				}
			})
		})
	}
}
