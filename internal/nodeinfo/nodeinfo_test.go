// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

func cpuResourceSlice(nodeName string) *resourcev1.ResourceSlice {
	device := func(name string, cpuID, coreID int64) resourcev1.Device {
		return resourcev1.Device{
			Name: name,
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To(cpuID)},
				nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To(coreID)},
				nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
				nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
			},
		}
	}
	return &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName + "-cpus"},
		Spec: resourcev1.ResourceSliceSpec{
			NodeName: ptr.To(nodeName),
			Pool: resourcev1.ResourcePool{
				Name:               nodeName,
				Generation:         1,
				ResourceSliceCount: 1,
			},
			Driver: nodeinfo.DraDriverCpu,
			Devices: []resourcev1.Device{
				device("cpu0", 0, 0),
				device("cpu1", 1, 0),
				device("cpu2", 2, 1),
				device("cpu3", 3, 1),
			},
		},
	}
}

func cpuClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithObjects(objects...).Build()
}

func TestNodeInfoGetCPUDeviceRequests(t *testing.T) {
	const deviceClassName = "my-cpus"
	ctx := context.Background()
	resources := &slurmcontrol.NodeResources{CoreBitmap: bitmaputil.String(bitmaputil.New(0))}
	want := []resourcev1.DeviceRequest{{
		Name: corev1.ResourceCPU.String(),
		Exactly: &resourcev1.ExactDeviceRequest{
			DeviceClassName: deviceClassName,
			AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
			Count:           2,
			Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{
					Expression: "device.attributes['dra.cpu'].cpuID in [0,1]",
				},
			}},
		},
	}}

	kubeClient := cpuClient(
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: deviceClassName}},
		cpuResourceSlice("node"),
	)
	node, err := nodeinfo.NewNodeInfo(ctx, kubeClient, "node")
	if err != nil {
		t.Fatalf("NewNodeInfo() error = %v", err)
	}
	got, err := node.GetCPUDeviceRequests(ctx, kubeClient, resources, deviceClassName)
	if err != nil {
		t.Fatalf("GetCPUDeviceRequests() error = %v", err)
	}
	if !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("GetCPUDeviceRequests() = %#v, want %#v", got, want)
	}
}

func TestNodeInfoGetCPUDeviceRequestsErrors(t *testing.T) {
	ctx := context.Background()
	resources := &slurmcontrol.NodeResources{CoreBitmap: bitmaputil.String(bitmaputil.New(0))}
	tests := []struct {
		name       string
		kubeClient client.Client
		want       string
	}{
		{
			name:       "missing DeviceClass",
			kubeClient: cpuClient(cpuResourceSlice("node")),
			want:       "was not found",
		},
		{
			name: "DeviceClass lookup failure",
			kubeClient: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return errors.New("injected lookup failure")
				},
			}).Build(),
			want: "injected lookup failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := nodeinfo.NewNodeInfo(ctx, tt.kubeClient, "node")
			if err != nil {
				t.Fatalf("NewNodeInfo() error = %v", err)
			}
			_, err = node.GetCPUDeviceRequests(ctx, tt.kubeClient, resources, nodeinfo.DraDriverCpu)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("GetCPUDeviceRequests() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestNodeInfoGetCPUDeviceRequestAllocationResults(t *testing.T) {
	ctx := context.Background()
	kubeClient := cpuClient(
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraDriverCpu}},
		cpuResourceSlice("node"),
	)
	node, err := nodeinfo.NewNodeInfo(ctx, kubeClient, "node")
	if err != nil {
		t.Fatalf("NewNodeInfo() error = %v", err)
	}
	resources := &slurmcontrol.NodeResources{CoreBitmap: bitmaputil.String(bitmaputil.New(0))}
	got, err := node.GetCPUDeviceRequestAllocationResults(ctx, kubeClient, resources, nodeinfo.DraDriverCpu)
	if err != nil {
		t.Fatalf("GetCPUDeviceRequestAllocationResults() error = %v", err)
	}
	want := []resourcev1.DeviceRequestAllocationResult{
		{Request: "cpu", Driver: nodeinfo.DraDriverCpu, Pool: "node", Device: "cpu0"},
		{Request: "cpu", Driver: nodeinfo.DraDriverCpu, Pool: "node", Device: "cpu1"},
	}
	if !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("GetCPUDeviceRequestAllocationResults() = %#v, want %#v", got, want)
	}
}

func TestNewNodeInfoIgnoresGPUResourceSlices(t *testing.T) {
	resourceSlices := []resourcev1.ResourceSlice{
		*cpuResourceSlice("node"),
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				NodeName: ptr.To("node"),
				Driver:   "gpu.nvidia.com",
				Devices:  []resourcev1.Device{{Name: "gpu-0"}},
			},
		},
	}

	node, err := nodeinfo.NewNodeInfoFromResourceSlices("node", resourceSlices)
	if err != nil {
		t.Fatalf("NewNodeInfoFromResourceSlices() error = %v", err)
	}
	if len(node.CpuMap.CPUInfoMap) != 4 {
		t.Fatalf("NewNodeInfoFromResourceSlices() CPU count = %d, want 4", len(node.CpuMap.CPUInfoMap))
	}
}

func TestNewNodeInfoFromResourceSlicesMergesLatestCompleteCPUPool(t *testing.T) {
	old := cpuResourceSlice("node")
	old.Name = "old"
	old.Spec.Devices = old.Spec.Devices[:1]

	currentA := cpuResourceSlice("node")
	currentA.Name = "00000-current"
	currentA.Spec.Pool.Generation = 2
	currentA.Spec.Pool.ResourceSliceCount = 2
	currentA.Spec.Devices = currentA.Spec.Devices[:2]

	currentB := cpuResourceSlice("node")
	currentB.Name = "00001-current"
	currentB.Spec.Pool.Generation = 2
	currentB.Spec.Pool.ResourceSliceCount = 2
	currentB.Spec.Devices = currentB.Spec.Devices[2:]

	node, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{*currentB, *old, *currentA})
	if err != nil {
		t.Fatalf("NewNodeInfoFromResourceSlices() error = %v", err)
	}
	if got := len(node.CpuMap.CPUInfoMap); got != 4 {
		t.Fatalf("NewNodeInfoFromResourceSlices() CPU count = %d, want 4", got)
	}
	if got := len(node.CpuMap.AbstractToMachine); got != 2 {
		t.Fatalf("NewNodeInfoFromResourceSlices() core count = %d, want 2", got)
	}
	if node.CpuMap.Pool != "node" {
		t.Fatalf("NewNodeInfoFromResourceSlices() pool = %q, want node", node.CpuMap.Pool)
	}
}

func TestNewNodeInfoFromResourceSlicesRejectsIncompleteLatestCPUPool(t *testing.T) {
	old := cpuResourceSlice("node")
	old.Name = "old"

	current := cpuResourceSlice("node")
	current.Name = "00000-current"
	current.Spec.Pool.Generation = 2
	current.Spec.Pool.ResourceSliceCount = 2
	current.Spec.Devices = current.Spec.Devices[:2]

	_, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{*old, *current})
	if err == nil || !strings.Contains(err.Error(), "generation 2 is incomplete: found 1 of 2 ResourceSlices") {
		t.Fatalf("NewNodeInfoFromResourceSlices() error = %v, want incomplete pool error", err)
	}
}
