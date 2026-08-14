// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
)

func nodeForTest(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func deviceIDForTest(driver, pool, device string) DeviceIdentity {
	return structured.MakeDeviceID(driver, pool, device)
}

func TestBuildNodeInventory(t *testing.T) {
	resourceSlice := func(nodeName, driver, pool string, devices ...string) resourcev1.ResourceSlice {
		slice := resourcev1.ResourceSlice{
			Spec: resourcev1.ResourceSliceSpec{
				NodeName: ptr.To(nodeName),
				Driver:   driver,
				Pool: resourcev1.ResourcePool{
					Name:               pool,
					Generation:         1,
					ResourceSliceCount: 1,
				},
			},
		}
		for _, name := range devices {
			slice.Spec.Devices = append(slice.Spec.Devices, resourcev1.Device{Name: name})
		}
		return slice
	}

	t.Run("builds deterministic indices across slices", func(t *testing.T) {
		slices := []resourcev1.ResourceSlice{
			resourceSlice("node-a", "gpu.example.com", "pool-b", "gpu-2", "gpu-0"),
			resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-3", "gpu-0"),
			resourceSlice("node-b", "gpu.example.com", "pool-other-node", "other-node-device"),
			resourceSlice("node-a", "unsupported.example.com", "pool-a", "unsupported-device"),
		}

		got, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nodeForTest("node-a"), slices)
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := DefaultRegistry().LookupByName("gpu-example")
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{{
				Profile: profile,
				Devices: []DeviceIdentity{
					deviceIDForTest("gpu.example.com", "pool-a", "gpu-0"),
					deviceIDForTest("gpu.example.com", "pool-a", "gpu-3"),
					deviceIDForTest("gpu.example.com", "pool-b", "gpu-0"),
					deviceIDForTest("gpu.example.com", "pool-b", "gpu-2"),
				},
			}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
	})

	t.Run("returns an empty inventory without matching devices", func(t *testing.T) {
		got, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nodeForTest("node-a"), []resourcev1.ResourceSlice{
			resourceSlice("node-b", "gpu.example.com", "pool-a", "gpu-0"),
		})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		want := NodeInventory{NodeName: "node-a"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
	})

	t.Run("rejects duplicate identities", func(t *testing.T) {
		_, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nodeForTest("node-a"), []resourcev1.ResourceSlice{
			resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-0", "gpu-0"),
		})
		if err == nil || !strings.Contains(err.Error(), "gpu.example.com/pool-a/gpu-0") {
			t.Fatalf("BuildNodeInventory() error = %v, want duplicate identity error", err)
		}
	})

	t.Run("uses a complete highest pool generation", func(t *testing.T) {
		old := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-old")
		currentA := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-1")
		currentA.Spec.Pool.Generation = 2
		currentA.Spec.Pool.ResourceSliceCount = 2
		currentB := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-0")
		currentB.Spec.Pool.Generation = 2
		currentB.Spec.Pool.ResourceSliceCount = 2

		got, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nodeForTest("node-a"), []resourcev1.ResourceSlice{
			currentA,
			old,
			currentB,
		})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := DefaultRegistry().LookupByName("gpu-example")
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{{
				Profile: profile,
				Devices: []DeviceIdentity{
					deviceIDForTest("gpu.example.com", "pool-a", "gpu-0"),
					deviceIDForTest("gpu.example.com", "pool-a", "gpu-1"),
				},
			}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
	})

	t.Run("rejects an incomplete highest pool generation", func(t *testing.T) {
		old := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-old")
		current := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-current")
		current.Spec.Pool.Generation = 2
		current.Spec.Pool.ResourceSliceCount = 2

		_, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nodeForTest("node-a"), []resourcev1.ResourceSlice{old, current})
		if err == nil || !strings.Contains(err.Error(), "generation 2 is incomplete: found 1 of 2 ResourceSlices") {
			t.Fatalf("BuildNodeInventory() error = %v, want incomplete pool error", err)
		}
	})

	t.Run("checks pool completeness before filtering by node", func(t *testing.T) {
		local := resourceSlice("node-a", "gpu.example.com", "shared-pool", "gpu-local")
		local.Spec.Pool.ResourceSliceCount = 2
		remote := resourceSlice("node-b", "gpu.example.com", "shared-pool", "gpu-remote")
		remote.Spec.Pool.ResourceSliceCount = 2

		got, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nodeForTest("node-a"), []resourcev1.ResourceSlice{local, remote})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := DefaultRegistry().LookupByName("gpu-example")
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{{
				Profile: profile,
				Devices: []DeviceIdentity{deviceIDForTest("gpu.example.com", "shared-pool", "gpu-local")},
			}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
	})

	t.Run("rejects a nil registry", func(t *testing.T) {
		_, err := BuildNodeInventory(context.Background(), nil, nodeForTest("node-a"), nil)
		if err == nil || !strings.Contains(err.Error(), "registry must not be nil") {
			t.Fatalf("BuildNodeInventory() error = %v, want nil registry error", err)
		}
	})

	t.Run("rejects a nil node", func(t *testing.T) {
		_, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nil, nil)
		if err == nil || !strings.Contains(err.Error(), "node must not be nil") {
			t.Fatalf("BuildNodeInventory() error = %v, want nil node error", err)
		}
	})

	t.Run("resolves each device against profiles for its driver", func(t *testing.T) {
		profileA := DeviceProfile{
			Name:     "gpu-a",
			Driver:   "gpu.example.com",
			Selector: `device.driver == "gpu.example.com" && device.attributes["gpu.example.com"].model == "a"`,
			Backend:  IndexedGRESBackend{GRESName: "gpu"},
		}
		profileB := DeviceProfile{
			Name:     "gpu-b",
			Driver:   "gpu.example.com",
			Selector: `device.driver == "gpu.example.com" && device.attributes["gpu.example.com"].model == "b"`,
			Backend:  IndexedGRESBackend{GRESName: "gpu"},
		}
		registry := &Registry{
			byName: map[string]DeviceProfile{
				profileA.Name: profileA,
				profileB.Name: profileB,
			},
		}
		slice := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-b", "gpu-unsupported", "gpu-a")
		models := []string{"b", "unsupported", "a"}
		for i := range slice.Spec.Devices {
			slice.Spec.Devices[i].Attributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				"model": {StringValue: ptr.To(models[i])},
			}
		}

		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{slice})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{
				{
					Profile: profileA,
					Devices: []DeviceIdentity{deviceIDForTest("gpu.example.com", "pool-a", "gpu-a")},
				},
				{
					Profile: profileB,
					Devices: []DeviceIdentity{deviceIDForTest("gpu.example.com", "pool-a", "gpu-b")},
				},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
	})

	t.Run("rejects overlapping profiles", func(t *testing.T) {
		profileA := DeviceProfile{
			Name:     "gpu-a",
			Driver:   "gpu.example.com",
			Selector: `device.driver == "gpu.example.com"`,
			Backend:  IndexedGRESBackend{GRESName: "gpu"},
		}
		profileB := profileA
		profileB.Name = "gpu-b"
		registry := &Registry{
			byName: map[string]DeviceProfile{
				profileA.Name: profileA,
				profileB.Name: profileB,
			},
		}
		_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{
			resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-0"),
		})
		if err == nil || !strings.Contains(err.Error(), `matches overlapping device profiles "gpu-a" and "gpu-b"`) {
			t.Fatalf("BuildNodeInventory() error = %v, want overlapping profile error", err)
		}
	})

	t.Run("uses per-device node selection", func(t *testing.T) {
		slice := resourceSlice("", "gpu.example.com", "pool-a", "gpu-local", "gpu-remote")
		slice.Spec.NodeName = nil
		slice.Spec.PerDeviceNodeSelection = ptr.To(true)
		slice.Spec.Devices[0].NodeName = ptr.To("node-a")
		slice.Spec.Devices[1].NodeName = ptr.To("node-b")

		got, err := BuildNodeInventory(context.Background(), DefaultRegistry(), nodeForTest("node-a"), []resourcev1.ResourceSlice{slice})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := DefaultRegistry().LookupByName("gpu-example")
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{{
				Profile: profile,
				Devices: []DeviceIdentity{deviceIDForTest("gpu.example.com", "pool-a", "gpu-local")},
			}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
	})
}
