// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"errors"
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
	registry := registryWithExampleGPU()
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

		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), slices)
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := registry.LookupByName("gpu-example")
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

	t.Run("classifies NVIDIA full GPUs", func(t *testing.T) {
		slice := resourceSlice("node-a", "gpu.nvidia.com", "node-a", "gpu-1", "gpu-0-mig-1g.10gb-0-0", "gpu-0")
		deviceTypes := []string{"gpu", "mig", "gpu"}
		for i := range slice.Spec.Devices {
			slice.Spec.Devices[i].Attributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				"type": {StringValue: ptr.To(deviceTypes[i])},
			}
		}

		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{slice})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := registry.LookupByName("gpu-nvidia")
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{{
				Profile: profile,
				Devices: []DeviceIdentity{
					deviceIDForTest("gpu.nvidia.com", "node-a", "gpu-0"),
					deviceIDForTest("gpu.nvidia.com", "node-a", "gpu-1"),
				},
			}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
	})

	t.Run("classifies CPU devices across multiple slices without turning them into GRES", func(t *testing.T) {
		first := resourceSlice("node-a", "dra.cpu", "node-a", "cpudev001")
		first.Spec.Pool.ResourceSliceCount = 2
		second := resourceSlice("node-a", "dra.cpu", "node-a", "cpudev000")
		second.Spec.Pool.ResourceSliceCount = 2

		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{first, second})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := registry.LookupByName("cpu")
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{{
				Profile: profile,
				Devices: []DeviceIdentity{
					deviceIDForTest("dra.cpu", "node-a", "cpudev000"),
					deviceIDForTest("dra.cpu", "node-a", "cpudev001"),
				},
			}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildNodeInventory() = %#v, want %#v", got, want)
		}
		gres, err := got.GRES()
		if err != nil {
			t.Fatalf("NodeInventory.GRES() error = %v", err)
		}
		if len(gres) != 0 {
			t.Fatalf("NodeInventory.GRES() = %#v, want no CPU GRES", gres)
		}
	})

	t.Run("returns an empty inventory without matching devices", func(t *testing.T) {
		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{
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
		_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{
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

		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{
			currentA,
			old,
			currentB,
		})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		profile, _ := registry.LookupByName("gpu-example")
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

		_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{old, current})
		if err == nil || !strings.Contains(err.Error(), "generation 2 is incomplete: found 1 of 2 ResourceSlices") {
			t.Fatalf("BuildNodeInventory() error = %v, want incomplete pool error", err)
		}
	})

	t.Run("ignores an incomplete pool that belongs to another node", func(t *testing.T) {
		local := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-local")
		remote := resourceSlice("node-b", "gpu.example.com", "pool-b", "gpu-remote")
		remote.Spec.Pool.ResourceSliceCount = 2

		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{local, remote})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		if len(got.Profiles) != 1 || len(got.Profiles[0].Devices) != 1 || got.Profiles[0].Devices[0].Device.String() != "gpu-local" {
			t.Fatalf("BuildNodeInventory() = %#v, want only gpu-local", got)
		}
	})

	t.Run("rejects per-device pools even without published local devices", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			devices []string
		}{
			{name: "remote devices", devices: []string{"gpu-remote"}},
			{name: "empty slice"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				local := resourceSlice("node-a", "gpu.example.com", "local-pool", "gpu-local")
				old := resourceSlice("", "gpu.example.com", "shared-pool", "gpu-old")
				old.Spec.NodeName = nil
				old.Spec.PerDeviceNodeSelection = ptr.To(true)
				old.Spec.Devices[0].NodeName = ptr.To("node-a")
				current := resourceSlice("", "gpu.example.com", "shared-pool", tt.devices...)
				current.Spec.NodeName = nil
				current.Spec.PerDeviceNodeSelection = ptr.To(true)
				current.Spec.Pool.Generation = 2
				current.Spec.Pool.ResourceSliceCount = 2
				for i := range current.Spec.Devices {
					current.Spec.Devices[i].NodeName = ptr.To("node-b")
				}

				_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{local, old, current})
				if err == nil || !strings.Contains(err.Error(), "unsupported spec.perDeviceNodeSelection") {
					t.Fatalf("BuildNodeInventory() error = %v, want unsupported node selection error", err)
				}
			})
		}
	})

	t.Run("rejects a complete per-device pool that belongs to another node", func(t *testing.T) {
		local := resourceSlice("node-a", "gpu.example.com", "local-pool", "gpu-local")
		remote := resourceSlice("", "gpu.example.com", "remote-pool", "gpu-remote")
		remote.Spec.NodeName = nil
		remote.Spec.PerDeviceNodeSelection = ptr.To(true)
		remote.Spec.Devices[0].NodeName = ptr.To("node-b")

		_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{local, remote})
		if err == nil || !strings.Contains(err.Error(), "unsupported spec.perDeviceNodeSelection") {
			t.Fatalf("BuildNodeInventory() error = %v, want unsupported node selection error", err)
		}
	})

	t.Run("rejects conflicting nodes in a pool generation", func(t *testing.T) {
		local := resourceSlice("node-a", "gpu.example.com", "shared-pool", "gpu-local")
		local.Spec.Pool.ResourceSliceCount = 2
		remote := resourceSlice("node-b", "gpu.example.com", "shared-pool", "gpu-remote")
		remote.Spec.Pool.ResourceSliceCount = 2

		for _, nodeName := range []string{"node-a", "node-b"} {
			for _, slices := range [][]resourcev1.ResourceSlice{{local, remote}, {remote, local}} {
				_, err := BuildNodeInventory(context.Background(), registry, nodeForTest(nodeName), slices)
				if err == nil || !strings.Contains(err.Error(), `pool "gpu.example.com/shared-pool" generation 1 has inconsistent nodeName`) {
					t.Fatalf("BuildNodeInventory() error = %v, want inconsistent nodeName error", err)
				}
			}
		}
	})

	t.Run("rejects an incomplete local generation with no devices yet", func(t *testing.T) {
		old := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-old")
		current := resourceSlice("node-a", "gpu.example.com", "pool-a")
		current.Spec.Pool.Generation = 2
		current.Spec.Pool.ResourceSliceCount = 2
		_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{old, current})
		if err == nil || !strings.Contains(err.Error(), "generation 2 is incomplete: found 1 of 2 ResourceSlices") {
			t.Fatalf("BuildNodeInventory() error = %v, want incomplete pool error", err)
		}
	})

	t.Run("rejects inconsistent slice counts in the latest generation", func(t *testing.T) {
		first := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-0")
		second := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-1")
		second.Spec.Pool.ResourceSliceCount = 2
		_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{first, second})
		if err == nil || !strings.Contains(err.Error(), "inconsistent resourceSliceCount") {
			t.Fatalf("BuildNodeInventory() error = %v, want inconsistent slice count error", err)
		}
	})

	t.Run("ignores obsolete invalid generations regardless of input order", func(t *testing.T) {
		oldA := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-old-a")
		oldB := resourceSlice("node-b", "gpu.example.com", "pool-a", "gpu-old-b")
		oldB.Spec.Pool.ResourceSliceCount = 2
		oldB.Spec.AllNodes = ptr.To(true)
		current := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-current")
		current.Spec.Pool.Generation = 2
		for _, slices := range [][]resourcev1.ResourceSlice{{oldA, oldB, current}, {current, oldB, oldA}, {oldB, current, oldA}} {
			got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), slices)
			if err != nil {
				t.Fatalf("BuildNodeInventory() error = %v", err)
			}
			if len(got.Profiles) != 1 || len(got.Profiles[0].Devices) != 1 || got.Profiles[0].Devices[0].Device.String() != "gpu-current" {
				t.Fatalf("BuildNodeInventory() = %#v, want only gpu-current", got)
			}
		}
	})

	t.Run("rejects a nil registry", func(t *testing.T) {
		_, err := BuildNodeInventory(context.Background(), nil, nodeForTest("node-a"), nil)
		if err == nil || !strings.Contains(err.Error(), "registry must not be nil") {
			t.Fatalf("BuildNodeInventory() error = %v, want nil registry error", err)
		}
	})

	t.Run("rejects a nil node", func(t *testing.T) {
		_, err := BuildNodeInventory(context.Background(), registry, nil, nil)
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
		registry, err := NewRegistry([]DeviceProfile{profileA, profileB})
		if err != nil {
			t.Fatalf("NewRegistry() error = %v", err)
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

	t.Run("classifies the default DRANET RDMA profile", func(t *testing.T) {
		slice := resourceSlice("node-a", "dra.net", "node-a", "rdma-ethernet", "infiniband", "dummy")
		pciAddress := func(address string) resourcev1.DeviceAttribute {
			return resourcev1.DeviceAttribute{StringValue: ptr.To(address)}
		}
		boolAttribute := func(value bool) resourcev1.DeviceAttribute {
			return resourcev1.DeviceAttribute{BoolValue: ptr.To(value)}
		}
		slice.Spec.Devices[0].Attributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			"dra.net/pciAddress": pciAddress("0000:01:00.0"),
			"dra.net/rdma":       boolAttribute(true),
		}
		slice.Spec.Devices[1].Attributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			"dra.net/pciAddress": pciAddress("0000:02:00.0"),
			"dra.net/rdma":       boolAttribute(true),
		}
		slice.Spec.Devices[2].Attributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			"dra.net/rdma": boolAttribute(false),
		}

		got, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{slice})
		if err != nil {
			t.Fatalf("BuildNodeInventory() error = %v", err)
		}
		rdmaProfile, _ := registry.LookupByName("dranet-rdma")
		want := NodeInventory{
			NodeName: "node-a",
			Profiles: []ProfileInventory{{
				Profile: rdmaProfile,
				Devices: []DeviceIdentity{
					deviceIDForTest("dra.net", "node-a", "infiniband"),
					deviceIDForTest("dra.net", "node-a", "rdma-ethernet"),
				},
			}},
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
		profileB.Selector = `device.driver == "gpu.example.com" && true`
		registry, registryErr := NewRegistry([]DeviceProfile{profileA, profileB})
		if registryErr != nil {
			t.Fatalf("NewRegistry() error = %v", registryErr)
		}
		_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{
			resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-0"),
		})
		if err == nil || !strings.Contains(err.Error(), `matches overlapping device profiles "gpu-a" and "gpu-b"`) {
			t.Fatalf("BuildNodeInventory() error = %v, want overlapping profile error", err)
		}
		var overlapErr *OverlappingDeviceProfilesError
		if !errors.As(err, &overlapErr) {
			t.Fatalf("BuildNodeInventory() error = %T, want *OverlappingDeviceProfilesError", err)
		}
		wantDevice := deviceIDForTest("gpu.example.com", "pool-a", "gpu-0")
		if overlapErr.Device != wantDevice || overlapErr.Profiles != [2]string{"gpu-a", "gpu-b"} {
			t.Fatalf("BuildNodeInventory() overlap error = %#v, want device %q and profiles [gpu-a gpu-b]", overlapErr, wantDevice.String())
		}
	})

	t.Run("requires explicit single node selection", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			mutate  func(*resourcev1.ResourceSlice)
			wantErr string
		}{
			{
				name:    "missing nodeName",
				mutate:  func(s *resourcev1.ResourceSlice) { s.Spec.NodeName = nil },
				wantErr: "nonempty spec.nodeName",
			},
			{
				name:    "empty nodeName",
				mutate:  func(s *resourcev1.ResourceSlice) { s.Spec.NodeName = ptr.To("") },
				wantErr: "nonempty spec.nodeName",
			},
			{
				name:    "nodeSelector",
				mutate:  func(s *resourcev1.ResourceSlice) { s.Spec.NodeSelector = &corev1.NodeSelector{} },
				wantErr: "unsupported spec.nodeSelector",
			},
			{
				name:    "allNodes",
				mutate:  func(s *resourcev1.ResourceSlice) { s.Spec.AllNodes = ptr.To(true) },
				wantErr: "unsupported spec.allNodes",
			},
			{
				name:    "perDeviceNodeSelection",
				mutate:  func(s *resourcev1.ResourceSlice) { s.Spec.PerDeviceNodeSelection = ptr.To(true) },
				wantErr: "unsupported spec.perDeviceNodeSelection",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				for _, withNodeName := range []bool{true, false} {
					slice := resourceSlice("node-a", "gpu.example.com", "pool-a", "gpu-local")
					slice.Name = "invalid-slice"
					if !withNodeName {
						slice.Spec.NodeName = nil
					}
					tt.mutate(&slice)
					_, err := BuildNodeInventory(context.Background(), registry, nodeForTest("node-a"), []resourcev1.ResourceSlice{slice})
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "invalid-slice") {
						t.Fatalf("BuildNodeInventory() error = %v, want ResourceSlice name and %q", err, tt.wantErr)
					}
				}
			})
		}
	})
}
