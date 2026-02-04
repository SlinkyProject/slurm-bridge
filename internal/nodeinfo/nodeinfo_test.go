// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

func init() {
	utilruntime.Must(scheme.AddToScheme(scheme.Scheme))
	utilruntime.Must(resourcev1.AddToScheme(scheme.Scheme))
}

func resourceSliceNodeIndex(obj client.Object) []string {
	rs, ok := obj.(*resourcev1.ResourceSlice)
	if !ok {
		return nil
	}
	nodeName := ptr.Deref(rs.Spec.NodeName, "")
	if nodeName == "" {
		return nil
	}
	return []string{nodeName}
}

func TestNodeInfo_GetDeviceRequests(t *testing.T) {
	tests := []struct {
		name       string
		kubeclient client.Client
		nodeName   string
		resources  *slurmcontrol.NodeResources
		want       []resourcev1.DeviceRequest
		wantErr    bool
	}{
		{
			name: "dra.cpu",
			kubeclient: fake.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
				WithObjects(
					&corev1.Node{
						ObjectMeta: metav1.ObjectMeta{Name: "node"},
					},
					&resourcev1.DeviceClass{
						ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraDriverCpu},
					},
					&resourcev1.ResourceSlice{
						ObjectMeta: metav1.ObjectMeta{Name: "node-slice"},
						Spec: resourcev1.ResourceSliceSpec{
							NodeName: ptr.To("node"),
							Driver:   nodeinfo.DraDriverCpu,
							Devices: []resourcev1.Device{
								{
									Name: "cpu0",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
								{
									Name: "cpu1",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](1)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
								{
									Name: "cpu2",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](2)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
								{
									Name: "cpu3",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](3)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
							},
						},
					},
				).
				Build(),
			nodeName: "node",
			resources: &slurmcontrol.NodeResources{
				Node:       "node",
				CoreBitmap: bitmaputil.String(bitmaputil.New(0)),
			},
			want: []resourcev1.DeviceRequest{
				{
					Name: "cpu",
					Exactly: &resourcev1.ExactDeviceRequest{
						DeviceClassName: nodeinfo.DraDriverCpu,
						AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
						Count:           2,
						Selectors: []resourcev1.DeviceSelector{
							{
								CEL: &resourcev1.CELDeviceSelector{
									Expression: "device.attributes['dra.cpu'].cpuID in [0,1]",
								},
							},
						},
					},
				},
			},
		},
		{
			name: "gpu.example.com",
			kubeclient: fake.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
				WithObjects(
					&corev1.Node{
						ObjectMeta: metav1.ObjectMeta{Name: "node"},
					},
					&resourcev1.DeviceClass{
						ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraExampleDriver},
					},
					&resourcev1.ResourceSlice{
						ObjectMeta: metav1.ObjectMeta{Name: "node-slice"},
						Spec: resourcev1.ResourceSliceSpec{
							NodeName: ptr.To("node"),
							Driver:   nodeinfo.DraExampleDriver,
							Devices: []resourcev1.Device{
								{
									Name: "gpu-0",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](0)},
									},
								},
								{
									Name: "gpu-1",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](1)},
									},
								},
								{
									Name: "gpu-2",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](2)},
									},
								},
								{
									Name: "gpu-3",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](3)},
									},
								},
							},
						},
					},
				).
				Build(),
			nodeName: "node",
			resources: &slurmcontrol.NodeResources{
				Node: "node",
				Gres: []slurmcontrol.GresLayout{
					{
						Name:  "gpu",
						Type:  nodeinfo.DraExampleDriver,
						Count: 2,
						Index: "0-1",
					},
				},
			},
			want: []resourcev1.DeviceRequest{
				{
					Name: "gpu",
					Exactly: &resourcev1.ExactDeviceRequest{
						DeviceClassName: nodeinfo.DraExampleDriver,
						AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
						Count:           2,
						Selectors: []resourcev1.DeviceSelector{
							{
								CEL: &resourcev1.CELDeviceSelector{
									Expression: "device.attributes['gpu.example.com'].index in [0,1]",
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := nodeinfo.NewNodeInfo(context.Background(), tt.kubeclient, tt.nodeName)
			if err != nil {
				t.Fatalf("could not construct receiver type: %v", err)
			}
			got, gotErr := n.GetDeviceRequests(context.Background(), tt.kubeclient, tt.resources)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("GetDeviceRequests() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("GetDeviceRequests() succeeded unexpectedly")
			}
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("GetDeviceRequests() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNodeInfo_GetDeviceRequestAllocationResult(t *testing.T) {
	tests := []struct {
		name       string
		kubeclient client.Client
		nodeName   string
		resources  *slurmcontrol.NodeResources
		want       []resourcev1.DeviceRequestAllocationResult
		wantErr    bool
	}{
		{
			name: "dra.cpu",
			kubeclient: fake.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
				WithObjects(
					&corev1.Node{
						ObjectMeta: metav1.ObjectMeta{Name: "node"},
					},
					&resourcev1.DeviceClass{
						ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraDriverCpu},
					},
					&resourcev1.ResourceSlice{
						ObjectMeta: metav1.ObjectMeta{Name: "node-slice"},
						Spec: resourcev1.ResourceSliceSpec{
							NodeName: ptr.To("node"),
							Driver:   nodeinfo.DraDriverCpu,
							Devices: []resourcev1.Device{
								{
									Name: "cpu0",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
								{
									Name: "cpu1",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](1)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
								{
									Name: "cpu2",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](2)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
								{
									Name: "cpu3",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To[int64](3)},
										nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To[int64](1)},
										nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
										nodeinfo.DraDriverCpu_CoreType: {IntValue: ptr.To(int64(nodeinfo.CoreTypeStandard))},
									},
								},
							},
						},
					},
				).
				Build(),
			nodeName: "node",
			resources: &slurmcontrol.NodeResources{
				Node:       "node",
				CoreBitmap: bitmaputil.String(bitmaputil.New(0)),
			},
			want: []resourcev1.DeviceRequestAllocationResult{
				{Request: "cpu", Driver: nodeinfo.DraDriverCpu, Device: "cpu0", Pool: "node"},
				{Request: "cpu", Driver: nodeinfo.DraDriverCpu, Device: "cpu1", Pool: "node"},
			},
		},
		{
			name: "gpu.example.com",
			kubeclient: fake.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
				WithObjects(
					&corev1.Node{
						ObjectMeta: metav1.ObjectMeta{Name: "node"},
					},
					&resourcev1.DeviceClass{
						ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraExampleDriver},
					},
					&resourcev1.ResourceSlice{
						ObjectMeta: metav1.ObjectMeta{Name: "node-slice"},
						Spec: resourcev1.ResourceSliceSpec{
							NodeName: ptr.To("node"),
							Driver:   nodeinfo.DraExampleDriver,
							Devices: []resourcev1.Device{
								{
									Name: "gpu-0",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](0)},
									},
								},
								{
									Name: "gpu-1",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](1)},
									},
								},
								{
									Name: "gpu-2",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](2)},
									},
								},
								{
									Name: "gpu-3",
									Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
										nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](3)},
									},
								},
							},
						},
					},
				).
				Build(),
			nodeName: "node",
			resources: &slurmcontrol.NodeResources{
				Node: "node",
				Gres: []slurmcontrol.GresLayout{
					{
						Name:  "gpu",
						Type:  nodeinfo.DraExampleDriver,
						Count: 2,
						Index: "0-1",
					},
				},
			},
			want: []resourcev1.DeviceRequestAllocationResult{
				{Request: "gpu", Driver: nodeinfo.DraExampleDriver, Device: "gpu-0", Pool: "node"},
				{Request: "gpu", Driver: nodeinfo.DraExampleDriver, Device: "gpu-1", Pool: "node"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := nodeinfo.NewNodeInfo(context.Background(), tt.kubeclient, tt.nodeName)
			if err != nil {
				t.Fatalf("could not construct receiver type: %v", err)
			}
			got, gotErr := n.GetDeviceRequestAllocationResult(context.Background(), tt.kubeclient, tt.resources)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("GetDeviceRequestAllocationResult() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("GetDeviceRequestAllocationResult() succeeded unexpectedly")
			}
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("GetDeviceRequestAllocationResult() = %v, want %v", got, tt.want)
			}
		})
	}
}
