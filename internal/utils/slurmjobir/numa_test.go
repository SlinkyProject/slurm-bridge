// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"errors"
	"fmt"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
)

func TestFormatGRESResourcesOrdersByNUMASpan(t *testing.T) {
	resources := map[dra.GRES]resource.Quantity{
		{Name: "gpu", Type: "gpu.nvidia.com"}:  resource.MustParse("2"),
		{Name: "nic", Type: "dranet0"}:         resource.MustParse("1"),
		{Name: "gpu", Type: "gpu.example.com"}: resource.MustParse("1"),
		{Name: "gpu"}:                          resource.MustParse("1"),
	}
	tests := []struct {
		name  string
		spans map[string]float64
		want  string
	}{
		{
			name: "name order without locality",
			want: "gres/gpu=1,gres/gpu:gpu.example.com=1,gres/gpu:gpu.nvidia.com=2,gres/nic:dranet0=1",
		},
		{
			name:  "narrowest NUMA spread first",
			spans: map[string]float64{"gpu.nvidia.com": 2, "dranet0": 1},
			want:  "gres/nic:dranet0=1,gres/gpu:gpu.nvidia.com=2,gres/gpu=1,gres/gpu:gpu.example.com=1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatGRESResources(resources, tt.spans); got != tt.want {
				t.Fatalf("formatGRESResources() = %q, want %q", got, tt.want)
			}
		})
	}
}

type failingResourceSliceLister struct{}

func (failingResourceSliceLister) List(labels.Selector) ([]*resourcev1.ResourceSlice, error) {
	return nil, errors.New("cache unavailable")
}

func TestNUMASpansUseCachedResourceSlices(t *testing.T) {
	registry, err := dra.NewRegistry([]dra.DeviceProfile{
		{Name: "gpu.example.com", Driver: "gpu.example.com", Selector: `device.driver == 'gpu.example.com'`, Backend: dra.IndexedGRESBackend{GRESName: "gpu"}},
		{Name: "nic.example.com", Driver: "nic.example.com", Selector: `device.driver == 'nic.example.com'`, Backend: dra.IndexedGRESBackend{GRESName: "nic"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	slice := func(driver string, numaNodes ...int64) *resourcev1.ResourceSlice {
		s := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: driver},
			Spec: resourcev1.ResourceSliceSpec{
				NodeName: ptr.To("node"), Driver: driver,
				Pool: resourcev1.ResourcePool{Name: "node", Generation: 1, ResourceSliceCount: 1},
			},
		}
		for i, numa := range numaNodes {
			s.Spec.Devices = append(s.Spec.Devices, resourcev1.Device{
				Name: fmt.Sprintf("dev-%d", i),
				Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
					deviceattribute.StandardDeviceAttributeNUMANode: {IntValues: []int64{numa}},
				},
			})
		}
		return s
	}
	cached := fake.NewClientBuilder().WithObjects(slice("gpu.example.com", 0, 1), slice("nic.example.com", 1)).Build()
	resources := map[dra.GRES]resource.Quantity{
		{Name: "gpu", Type: "gpu.example.com"}: resource.MustParse("1"),
		{Name: "nic", Type: "nic.example.com"}: resource.MustParse("1"),
	}
	for _, tc := range []struct {
		name   string
		lister ResourceSliceLister
		want   string
	}{
		{"cached slices", CachedResourceSlices(context.Background(), cached), "gres/nic:nic.example.com=1,gres/gpu:gpu.example.com=1"},
		{"no lister", nil, "gres/gpu:gpu.example.com=1,gres/nic:nic.example.com=1"},
		{"lister error", failingResourceSliceLister{}, "gres/gpu:gpu.example.com=1,gres/nic:nic.example.com=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &translator{ctx: context.Background(), draRegistry: registry}
			WithResourceSlices(tc.lister)(tr)
			if got := formatGRESResources(resources, tr.numaSpans(resources)); got != tc.want {
				t.Fatalf("formatGRESResources() = %q, want %q", got, tc.want)
			}
		})
	}
}
