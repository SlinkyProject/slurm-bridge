// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resourcelisters "k8s.io/client-go/listers/resource/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestNativeGangPreservesNUMAGRESOrdering(t *testing.T) {
	sb, control, pods, _ := nativeFixture(t, slurmjobir.WorkloadAPIVersionV1Beta1)
	profiles := []dra.DeviceProfile{
		{Name: "gpu.example.com", Driver: "gpu.example.com", Selector: `device.driver == 'gpu.example.com'`, Backend: dra.IndexedGRESBackend{GRESName: "gpu"}},
		{Name: "nic.example.com", Driver: "nic.example.com", Selector: `device.driver == 'nic.example.com'`, Backend: dra.IndexedGRESBackend{GRESName: "nic"}},
	}
	var err error
	sb.draRegistry, err = dra.NewRegistry(profiles)
	if err != nil {
		t.Fatal(err)
	}
	index := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for i, profile := range profiles {
		class := &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: profile.Name},
			Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: profile.Selector},
			}}},
		}
		if err := sb.Create(t.Context(), class); err != nil {
			t.Fatal(err)
		}
		slice := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: profile.Name},
			Spec: resourcev1.ResourceSliceSpec{
				Driver: profile.Driver, NodeName: ptr.To("node1"),
				Pool: resourcev1.ResourcePool{Name: "node1", Generation: 1, ResourceSliceCount: 1},
			},
		}
		// GPUs span two NUMA nodes, while NICs occupy only one. Locality must
		// therefore put the NIC GRES first, reversing the default name order.
		for numa := range 2 - i {
			slice.Spec.Devices = append(slice.Spec.Devices, resourcev1.Device{
				Name: fmt.Sprintf("dev-%d", numa),
				Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
					deviceattribute.StandardDeviceAttributeNUMANode: {IntValues: []int64{int64(numa)}},
				},
			})
		}
		if err := index.Add(slice); err != nil {
			t.Fatal(err)
		}
	}
	sb.resourceSlices = resourcelisters.NewResourceSliceLister(index)
	for _, pod := range pods {
		pod.Spec.Containers = []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + profiles[0].Name): resource.MustParse("1"),
				corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + profiles[1].Name): resource.MustParse("1"),
			},
		}}}
		if err := sb.Update(t.Context(), pod); err != nil {
			t.Fatal(err)
		}
	}
	control.job.Pending = true
	control.job.Nodes = ""
	group := framework.NewCycleState()
	for _, pod := range pods {
		state := nativePodState(group)
		if _, status := sb.PreFilter(t.Context(), state, pod, nil); !status.IsSuccess() {
			t.Fatalf("PreFilter: %v", status)
		}
		data, err := getStateData(state)
		if err != nil {
			t.Fatal(err)
		}
		got := ptr.Deref(data.slurmJobIR.Components[0].JobInfo.Gres, "")
		if want := "gres/nic:nic.example.com=1,gres/gpu:gpu.example.com=1"; got != want {
			t.Fatalf("native gang GRES = %q, want %q", got, want)
		}
	}
	if got := control.allocationReads.Load(); got != 1 {
		t.Fatalf("allocation reads = %d, want 1 shared gang decision", got)
	}
}
