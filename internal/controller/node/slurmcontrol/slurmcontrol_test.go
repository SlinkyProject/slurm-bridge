// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func init() {
	utilruntime.Must(resourcev1.AddToScheme(scheme.Scheme))
}

func testNodeCPUResourceSlice(nodeName string) *resourcev1.ResourceSlice {
	coreType := ptr.To(nodeinfo.CoreTypeStandard.String())
	device := func(name string, cpuID, coreID int64) resourcev1.Device {
		return resourcev1.Device{
			Name: name,
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To(cpuID)},
				nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To(coreID)},
				nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
				nodeinfo.DraDriverCpu_CoreType: {StringValue: coreType},
			},
		}
	}
	return &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName + "-dra-cpu"},
		Spec: resourcev1.ResourceSliceSpec{
			NodeName: ptr.To(nodeName),
			Driver:   nodeinfo.DraDriverCpu,
			Pool: resourcev1.ResourcePool{
				Name:               nodeName,
				Generation:         1,
				ResourceSliceCount: 1,
			},
			Devices: []resourcev1.Device{
				device("cpu0", 0, 0),
				device("cpu1", 1, 0),
				device("cpu2", 2, 1),
				device("cpu3", 3, 1),
			},
		},
	}
}

func testNodeInfoFromResourceSlices(t *testing.T, nodeName string, resourceSlices []resourcev1.ResourceSlice) *nodeinfo.NodeInfo {
	t.Helper()
	info, err := nodeinfo.NewNodeInfoFromResourceSlices(nodeName, resourceSlices)
	if err != nil {
		t.Fatalf("NewNodeInfoFromResourceSlices() error = %v", err)
	}
	return info
}

func testExampleDRAInventory() []dra.GRESInventory {
	return []dra.GRESInventory{{
		GRES: dra.GRES{Name: "gpu", Type: "gpu.example.com"},
		Devices: []dra.DeviceIdentity{
			structured.MakeDeviceID("gpu.example.com", "pool-a", "gpu-0"),
			structured.MakeDeviceID("gpu.example.com", "pool-a", "gpu-1"),
		},
	}}
}

func Test_realSlurmControl_GetNodeNames(t *testing.T) {
	ctx := context.Background()
	type fields struct {
		Client slurmclient.Client
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "Empty",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx: ctx,
			},
			want:    []string{},
			wantErr: false,
		},
		{
			name: "Not empty",
			fields: fields{
				Client: func() slurmclient.Client {
					list := &types.V0044NodeList{
						Items: []types.V0044Node{
							{V0044Node: api.V0044Node{Name: ptr.To("node-0")}},
							{V0044Node: api.V0044Node{Name: ptr.To("node-1")}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return c
				}(),
			},
			args: args{
				ctx: ctx,
			},
			want:    []string{"node-0", "node-1"},
			wantErr: false,
		},
		{
			name: "Failure",
			fields: fields{
				Client: func() slurmclient.Client {
					f := interceptor.Funcs{
						List: func(ctx context.Context, list object.ObjectList, opts ...slurmclient.ListOption) error {
							return fmt.Errorf("failed to list resources")
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
					return c
				}(),
			},
			args: args{
				ctx: ctx,
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			got, err := r.GetNodeNames(tt.args.ctx)
			slices.Sort(got)
			slices.Sort(tt.want)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.GetNodeNames() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetNodeNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_NodeExists(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0"}}
	tests := []struct {
		name    string
		client  slurmclient.Client
		want    bool
		wantErr bool
	}{
		{
			name:   "node exists",
			client: fake.NewClientBuilder().WithObjects(&types.V0044Node{V0044Node: api.V0044Node{Name: ptr.To("worker-0")}}).Build(),
			want:   true,
		},
		{
			name:   "node does not exist",
			client: fake.NewFakeClient(),
			want:   false,
		},
		{
			name: "get error",
			client: func() slurmclient.Client {
				f := interceptor.Funcs{
					Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
						return errors.New("get failed")
					},
				}
				return fake.NewClientBuilder().WithInterceptorFuncs(f).Build()
			}(),
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{Client: tt.client}
			got, err := r.NodeExists(ctx, node)
			if (err != nil) != tt.wantErr {
				t.Errorf("NodeExists() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NodeExists() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_MakeNodeDrain(t *testing.T) {
	type fields struct {
		Client slurmclient.Client
	}
	type args struct {
		ctx    context.Context
		node   *corev1.Node
		reason string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "not found",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
		{
			name: "found",
			fields: func() fields {
				node := &types.V0044Node{V0044Node: api.V0044Node{Name: ptr.To("node-0")}}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
		{
			name: "node already in DRAIN state skips update",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE, api.V0044NodeStateDRAIN}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
		{
			name: "update fails",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE}),
					},
				}
				f := interceptor.Funcs{
					Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
						return errors.New("update failed")
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).WithInterceptorFuncs(f).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			if err := r.MakeNodeDrain(tt.args.ctx, tt.args.node, tt.args.reason); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.MakeNodeDrain() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_MakeNodeUndrain(t *testing.T) {
	type fields struct {
		Client slurmclient.Client
	}
	type args struct {
		ctx    context.Context
		node   *corev1.Node
		reason string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "not found",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
		{
			name: "found",
			fields: func() fields {
				node := &types.V0044Node{V0044Node: api.V0044Node{Name: ptr.To("node-0")}}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			if err := r.MakeNodeUndrain(tt.args.ctx, tt.args.node, tt.args.reason); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.MakeNodeUndrain() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_IsNodeDrain(t *testing.T) {
	type fields struct {
		Client slurmclient.Client
	}
	type args struct {
		ctx  context.Context
		node *corev1.Node
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "not found",
			fields: func() fields {
				return fields{
					Client: fake.NewFakeClient(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "not drain",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "is drain",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE, api.V0044NodeStateDRAIN}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			got, err := r.IsNodeDrain(tt.args.ctx, tt.args.node)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.IsNodeDrain() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("realSlurmControl.IsNodeDrain() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_IsNodeExternal(t *testing.T) {
	type fields struct {
		Client slurmclient.Client
	}
	type args struct {
		ctx  context.Context
		node *corev1.Node
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "not found",
			fields: func() fields {
				return fields{
					Client: fake.NewFakeClient(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "not external",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "is external",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE, api.V0044NodeStateEXTERNAL}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			got, err := r.IsNodeExternal(tt.args.ctx, tt.args.node)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.IsNodeExternal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("realSlurmControl.IsNodeExternal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_IsNodeDrained(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}}
	tests := []struct {
		name    string
		client  slurmclient.Client
		want    bool
		wantErr bool
	}{
		{
			name:    "node not found",
			client:  fake.NewFakeClient(),
			want:    false,
			wantErr: true,
		},
		{
			name: "not drained - node idle",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE}),
					},
				},
			).Build(),
			want: false,
		},
		{
			name: "drained and idle - eligible for removal",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE, api.V0044NodeStateDRAIN}),
					},
				},
			).Build(),
			want: true,
		},
		{
			name: "drained but busy - has ALLOCATED",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateDRAIN, api.V0044NodeStateALLOCATED}),
					},
				},
			).Build(),
			want: false,
		},
		{
			name: "drained but busy - has MIXED",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateDRAIN, api.V0044NodeStateMIXED}),
					},
				},
			).Build(),
			want: false,
		},
		{
			name: "drain with undrain - not considered drained",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]api.V0044NodeState{api.V0044NodeStateDRAIN, api.V0044NodeStateUNDRAIN}),
					},
				},
			).Build(),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{Client: tt.client}
			got, err := r.IsNodeDrained(ctx, node)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsNodeDrained() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsNodeDrained() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_NodeNeedsRecreate(t *testing.T) {
	ctx := context.Background()

	makeNode := func(name string, cpu, memoryGi int64) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewQuantity(cpu, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(memoryGi*1024*1024*1024, resource.BinarySI),
				},
			},
		}
	}

	tests := []struct {
		name         string
		client       slurmclient.Client
		node         *corev1.Node
		nodeInfo     *nodeinfo.NodeInfo
		draInventory []dra.GRESInventory
		want         bool
		wantErr      bool
		wantErrText  string
	}{
		{
			name:   "node does not exist in Slurm",
			client: fake.NewFakeClient(),
			node:   makeNode("worker-0", 4, 8),
			want:   false,
		},
		{
			name: "node exists, same cpu memory gres",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       nil,
					},
				},
			).Build(),
			node: makeNode("worker-0", 4, 8),
			want: false,
		},
		{
			name: "node exists, different cpus",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       nil,
					},
				},
			).Build(),
			node: makeNode("worker-0", 8, 8),
			want: true,
		},
		{
			name: "node exists with matching DRA CPU topology",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Sockets:    ptr.To(int32(1)),
						Cores:      ptr.To(int32(2)),
						Threads:    ptr.To(int32(2)),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
					},
				},
			).Build(),
			node:     makeNode("worker-0", 12, 8),
			nodeInfo: testNodeInfoFromResourceSlices(t, "worker-0", []resourcev1.ResourceSlice{*testNodeCPUResourceSlice("worker-0")}),
			want:     false,
		},
		{
			name: "node exists with different DRA CPU topology",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Sockets:    ptr.To(int32(1)),
						Cores:      ptr.To(int32(4)),
						Threads:    ptr.To(int32(1)),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
					},
				},
			).Build(),
			node:     makeNode("worker-0", 12, 8),
			nodeInfo: testNodeInfoFromResourceSlices(t, "worker-0", []resourcev1.ResourceSlice{*testNodeCPUResourceSlice("worker-0")}),
			want:     true,
		},
		{
			name: "node exists, different memory",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       nil,
					},
				},
			).Build(),
			node: makeNode("worker-0", 4, 16),
			want: true,
		},
		{
			name: "hybrid node preserves unmanaged gres",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:driver:1"),
					},
				},
			).Build(),
			node: makeNode("worker-0", 4, 8),
			want: false,
		},
		{
			name: "external node recreates for different gres",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:driver:1"),
						State:      ptr.To([]api.V0044NodeState{api.V0044NodeStateEXTERNAL}),
					},
				},
			).Build(),
			node: makeNode("worker-0", 4, 8),
			want: true,
		},
		{
			name: "node exists with matching profile inventory",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:gpu.example.com:2"),
						Extra:      ptr.To(`slurm-bridge.dra-gres-map={"v":2,"profiles":{"gpu.example.com":{"firstIndex":0,"devices":["/dra/gpu.example.com/pool-a/gpu-0","/dra/gpu.example.com/pool-a/gpu-1"]}}}`),
					},
				},
			).Build(),
			node:         makeNode("worker-0", 4, 8),
			draInventory: testExampleDRAInventory(),
			want:         false,
		},
		{
			name: "node exists with stale applied profile inventory",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:gpu.example.com:2"),
						Extra:      ptr.To(`slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":["/dra/gpu.example.com/pool-a/gpu-0"]}}`),
					},
				},
			).Build(),
			node:         makeNode("worker-0", 4, 8),
			draInventory: testExampleDRAInventory(),
			want:         false,
		},
		{
			name: "node exists with removed profile inventory",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Extra:      ptr.To(`slurm-bridge.dra-gres-map={"v":1,"profiles":{}}`),
					},
				},
			).Build(),
			node: makeNode("worker-0", 4, 8),
			want: false,
		},
		{
			name: "hybrid node accepts additional gres",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:gpu.example.com:2,nic:infiniband:1"),
					},
				},
			).Build(),
			node:         makeNode("worker-0", 4, 8),
			draInventory: testExampleDRAInventory(),
			want:         false,
		},
		{
			name: "hybrid node accepts gres topology suffix",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:gpu.example.com:2(S:0-1)"),
					},
				},
			).Build(),
			node:         makeNode("worker-0", 4, 8),
			draInventory: testExampleDRAInventory(),
			want:         false,
		},
		{
			name: "hybrid node rejects incompatible gres with configuration hint",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:gpu.example.com:1,nic:infiniband:1"),
					},
				},
			).Build(),
			node:         makeNode("worker-0", 4, 8),
			draInventory: testExampleDRAInventory(),
			wantErr:      true,
			wantErrText:  "NodeName=worker-0 Name=gpu Type=gpu.example.com Count=2",
		},
		{
			name: "node exists with unrelated extra and no profile inventory",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Extra:      ptr.To("owned by an administrator"),
					},
				},
			).Build(),
			node: makeNode("worker-0", 4, 8),
			want: false,
		},
		{
			name: "node exists with unrelated extra and profile inventory",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       ptr.To("gpu:gpu.example.com:2"),
						Extra:      ptr.To("owned by an administrator"),
					},
				},
			).Build(),
			node:         makeNode("worker-0", 4, 8),
			draInventory: testExampleDRAInventory(),
			wantErr:      true,
			wantErrText:  `cannot record applied DRA inventory on Slurm node "worker-0": Extra field is already in use`,
		},
		{
			name: "get error",
			client: func() slurmclient.Client {
				f := interceptor.Funcs{
					Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
						return errors.New("get failed")
					},
				}
				return fake.NewClientBuilder().WithInterceptorFuncs(f).Build()
			}(),
			node:    makeNode("worker-0", 4, 8),
			want:    false,
			wantErr: true,
		},
		{
			name: "node uses slurm node name label",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("slurm-worker-0"),
						Cpus:       ptr.To(int32(4)),
						RealMemory: ptr.To(int64(8192)),
						Gres:       nil,
					},
				},
			).Build(),
			node: func() *corev1.Node {
				n := makeNode("k8s-worker-0", 4, 8)
				n.Labels = map[string]string{wellknown.LabelSlurmNodeName: "slurm-worker-0"}
				return n
			}(),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{Client: tt.client}
			got, err := r.NodeNeedsRecreate(ctx, tt.node, tt.nodeInfo, tt.draInventory)
			if (err != nil) != tt.wantErr {
				t.Errorf("NodeNeedsRecreate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErrText != "" && !strings.Contains(err.Error(), tt.wantErrText) {
				t.Errorf("NodeNeedsRecreate() error = %v, want containing %q", err, tt.wantErrText)
			}
			if got != tt.want {
				t.Errorf("NodeNeedsRecreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_NodeNeedsRecreate_ExternalGRES(t *testing.T) {
	inventory := append([]dra.GRESInventory{{
		GRES: dra.GRES{Name: "nic", Type: "dranet0"},
		Devices: []dra.DeviceIdentity{
			structured.MakeDeviceID("dra.net", "pool-a", "nic-0"),
		},
	}}, testExampleDRAInventory()...)
	extra, err := dra.EncodeAppliedInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-0"},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		}},
	}
	for _, tt := range []struct {
		name  string
		gres  string
		extra string
		want  bool
	}{
		{name: "same order", gres: "nic:dranet0:1,gpu:gpu.example.com:2", extra: extra},
		{name: "Slurm reorders entries", gres: "gpu:gpu.example.com:2,nic:dranet0:1", extra: extra},
		{name: "changed count", gres: "gpu:gpu.example.com:1,nic:dranet0:1", extra: extra, want: true},
		{name: "changed type", gres: "gpu:other:2,nic:dranet0:1", extra: extra, want: true},
		{name: "missing resource", gres: "gpu:gpu.example.com:2", extra: extra, want: true},
		{name: "duplicate resource", gres: "gpu:gpu.example.com:2,nic:dranet0:1,nic:dranet0:1", extra: extra, want: true},
		{name: "stale applied inventory", gres: "gpu:gpu.example.com:2,nic:dranet0:1", extra: `slurm-bridge.dra-gres-map={"v":1,"profiles":{}}`, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			slurmClient := fake.NewClientBuilder().WithObjects(&types.V0044Node{V0044Node: api.V0044Node{
				Name:       ptr.To(node.Name),
				Cpus:       ptr.To(int32(4)),
				RealMemory: ptr.To(int64(8192)),
				State:      ptr.To([]api.V0044NodeState{api.V0044NodeStateEXTERNAL}),
				Gres:       ptr.To(tt.gres),
				Extra:      ptr.To(tt.extra),
			}}).Build()
			r := &realSlurmControl{Client: slurmClient}
			got, err := r.NodeNeedsRecreate(context.Background(), node, nil, inventory)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("NodeNeedsRecreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_RemoveNode(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		client  slurmclient.Client
		node    *corev1.Node
		wantErr bool
	}{
		{
			name:   "node does not exist - tolerated",
			client: fake.NewFakeClient(),
			node:   &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0"}},
		},
		{
			name: "node exists - removed",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{Name: ptr.To("worker-0")},
				},
			).Build(),
			node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0"}},
		},
		{
			name: "get error",
			client: func() slurmclient.Client {
				f := interceptor.Funcs{
					Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
						return errors.New("get failed")
					},
				}
				return fake.NewClientBuilder().WithInterceptorFuncs(f).Build()
			}(),
			node:    &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0"}},
			wantErr: true,
		},
		{
			name: "delete error",
			client: func() slurmclient.Client {
				f := interceptor.Funcs{
					Delete: func(ctx context.Context, obj object.Object, opts ...slurmclient.DeleteOption) error {
						return errors.New("delete failed")
					},
				}
				return fake.NewClientBuilder().
					WithObjects(&types.V0044Node{V0044Node: api.V0044Node{Name: ptr.To("worker-0")}}).
					WithInterceptorFuncs(f).
					Build()
			}(),
			node:    &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0"}},
			wantErr: true,
		},
		{
			name: "node uses slurm node name label",
			client: fake.NewClientBuilder().WithObjects(
				&types.V0044Node{
					V0044Node: api.V0044Node{Name: ptr.To("slurm-worker-0")},
				},
			).Build(),
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "k8s-worker-0",
					Labels: map[string]string{wellknown.LabelSlurmNodeName: "slurm-worker-0"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{Client: tt.client}
			err := r.RemoveNode(ctx, tt.node)
			if (err != nil) != tt.wantErr {
				t.Errorf("RemoveNode() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_featuresEqual(t *testing.T) {
	tests := []struct {
		name    string
		current *api.V0044CsvString
		desired []string
		want    bool
	}{
		{"nil and empty", nil, nil, true},
		{"nil and non-empty", nil, []string{"a"}, false},
		{"empty and empty", ptr.To(api.V0044CsvString{}), []string{}, true},
		{"same order", ptr.To(api.V0044CsvString{"a", "b"}), []string{"a", "b"}, true},
		{"different order", ptr.To(api.V0044CsvString{"b", "a"}), []string{"a", "b"}, true},
		{"different length", ptr.To(api.V0044CsvString{"a"}), []string{"a", "b"}, false},
		{"different elements", ptr.To(api.V0044CsvString{"a", "b"}), []string{"a", "c"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := featuresEqual(tt.current, tt.desired); got != tt.want {
				t.Errorf("featuresEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_validatePartitionExists(t *testing.T) {
	type fields struct {
		Client slurmclient.Client
	}
	type args struct {
		ctx           context.Context
		partitionName string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "partition exists",
			fields: func() fields {
				partition := &types.V0044PartitionInfo{
					V0044PartitionInfo: api.V0044PartitionInfo{
						Name: ptr.To("slurm-bridge"),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(partition).Build(),
				}
			}(),
			args: args{
				ctx:           context.TODO(),
				partitionName: "slurm-bridge",
			},
			wantErr: false,
		},
		{
			name: "partition does not exist",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx:           context.TODO(),
				partitionName: "nonexistent",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			if err := r.validatePartitionExists(tt.args.ctx, tt.args.partitionName); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.validatePartitionExists() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_AddNode(t *testing.T) {
	type fields struct {
		Client slurmclient.Client
	}
	type args struct {
		ctx      context.Context
		node     *corev1.Node
		nodeInfo *nodeinfo.NodeInfo
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "add node without partition label",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "add node with valid partition annotation",
			fields: func() fields {
				partition := &types.V0044PartitionInfo{
					V0044PartitionInfo: api.V0044PartitionInfo{
						Name: ptr.To("slurm-bridge"),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(partition).Build(),
				}
			}(),
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Annotations: map[string]string{
							wellknown.AnnotationExternalNodePartitions: "slurm-bridge",
						},
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "add node with multiple valid partition annotations",
			fields: func() fields {
				partition1 := &types.V0044PartitionInfo{
					V0044PartitionInfo: api.V0044PartitionInfo{
						Name: ptr.To("slurm-bridge"),
					},
				}
				partition2 := &types.V0044PartitionInfo{
					V0044PartitionInfo: api.V0044PartitionInfo{
						Name: ptr.To("gpu"),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(partition1, partition2).Build(),
				}
			}(),
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Annotations: map[string]string{
							wellknown.AnnotationExternalNodePartitions: "slurm-bridge,gpu",
						},
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "add node with invalid partition annotation",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Annotations: map[string]string{
							wellknown.AnnotationExternalNodePartitions: "nonexistent",
						},
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "node already in Slurm with matching features skips update",
			fields: func() fields {
				partition := &types.V0044PartitionInfo{
					V0044PartitionInfo: api.V0044PartitionInfo{
						Name: ptr.To("slurm-bridge"),
					},
				}
				existingNode := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("test-node"),
						Features:   ptr.To(api.V0044CsvString{"slurm-bridge"}),
						Partitions: ptr.To(api.V0044CsvString{"slurm-bridge"}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(partition, existingNode).Build(),
				}
			}(),
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Annotations: map[string]string{
							wellknown.AnnotationExternalNodePartitions: "slurm-bridge",
						},
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "node already in Slurm with different features reconfigure Get fails",
			fields: func() fields {
				partition := &types.V0044PartitionInfo{
					V0044PartitionInfo: api.V0044PartitionInfo{
						Name: ptr.To("slurm-bridge"),
					},
				}
				existingNode := &types.V0044Node{
					V0044Node: api.V0044Node{
						Name:       ptr.To("test-node"),
						Features:   ptr.To(api.V0044CsvString{"old-partition"}),
						Partitions: ptr.To(api.V0044CsvString{"old-partition"}),
					},
				}
				reconfigureKey := (&types.V0044Reconfigure{}).GetKey()
				f := interceptor.Funcs{
					Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
						if key == reconfigureKey {
							return errors.New("reconfigure failed")
						}
						return nil
					},
				}
				return fields{
					Client: fake.NewClientBuilder().
						WithObjects(partition, existingNode).
						WithInterceptorFuncs(f).
						Build(),
				}
			}(),
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Annotations: map[string]string{
							wellknown.AnnotationExternalNodePartitions: "slurm-bridge",
						},
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "node with nodeInfo but no CPU ResourceSlice (DRA CPU driver not installed)",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
				nodeInfo: func() *nodeinfo.NodeInfo {
					kubeClient := ctrlclientfake.NewClientBuilder().
						WithScheme(scheme.Scheme).
						Build()
					info, err := nodeinfo.NewNodeInfo(context.Background(), kubeClient, "test-node")
					if err != nil {
						t.Fatalf("NewNodeInfo: %v", err)
					}
					return info
				}(),
			},
			wantErr: false,
		},
		{
			name: "add node with CPU topology from NewNodeInfo",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				},
				nodeInfo: func() *nodeinfo.NodeInfo {
					kubeClient := ctrlclientfake.NewClientBuilder().
						WithScheme(scheme.Scheme).
						WithObjects(testNodeCPUResourceSlice("test-node")).
						Build()
					info, err := nodeinfo.NewNodeInfo(context.Background(), kubeClient, "test-node")
					if err != nil {
						t.Fatalf("NewNodeInfo: %v", err)
					}
					return info
				}(),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			if err := r.AddNode(tt.args.ctx, tt.args.node, tt.args.nodeInfo, nil); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.AddNode() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_AddNode_includesAppliedDRAInventory(t *testing.T) {
	var nodeConf string
	var extra string
	var comment *string
	f := interceptor.Funcs{
		Create: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.CreateOption) error {
			if r, ok := req.(api.V0044OpenapiCreateNodeReq); ok {
				nodeConf = r.NodeConf
			}
			return nil
		},
		Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
			if r, ok := req.(api.V0044UpdateNodeMsg); ok {
				extra = ptr.Deref(r.Extra, "")
				comment = r.Comment
			}
			return nil
		},
	}
	r := &realSlurmControl{Client: fake.NewClientBuilder().WithInterceptorFuncs(f).Build()}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		}},
	}
	if err := r.AddNode(context.Background(), node, nil, testExampleDRAInventory()); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	wants := []string{
		`Feature=slurm_bridge_gres_compatible`,
		`Gres="gpu:gpu.example.com:2"`,
		`GresConf="count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-0+count=1,name=gpu,type=gpu.example.com,file=/dra/gpu.example.com/pool-a/gpu-1"`,
	}
	for _, want := range wants {
		if !strings.Contains(nodeConf, want) {
			t.Errorf("NodeConf missing %q: %q", want, nodeConf)
		}
	}
	wantExtra := `slurm-bridge.dra-gres-map={"v":2,"profiles":{"gpu.example.com":{"firstIndex":0,"devices":["/dra/gpu.example.com/pool-a/gpu-0","/dra/gpu.example.com/pool-a/gpu-1"]}}}`
	if extra != wantExtra {
		t.Errorf("AddNode() extra = %q, want %q", extra, wantExtra)
	}
	if comment != nil {
		t.Errorf("AddNode() comment = %q, want nil", ptr.Deref(comment, ""))
	}
}

func Test_realSlurmControl_AddNode_addsGRESCompatibilityFeatureToExistingExternalNode(t *testing.T) {
	var featureUpdate *api.V0044UpdateNodeMsg
	f := interceptor.Funcs{
		Update: func(_ context.Context, _ object.Object, req any, _ ...slurmclient.UpdateOption) error {
			update := req.(api.V0044UpdateNodeMsg)
			if update.Features != nil || update.FeaturesAct != nil {
				featureUpdate = &update
			}
			return nil
		},
	}
	existingNode := &types.V0044Node{V0044Node: api.V0044Node{
		Name:           ptr.To("test-node"),
		State:          ptr.To([]api.V0044NodeState{api.V0044NodeStateEXTERNAL}),
		Features:       ptr.To(api.V0044CsvString{"admin-feature"}),
		ActiveFeatures: ptr.To(api.V0044CsvString{"admin-feature"}),
	}}
	r := &realSlurmControl{Client: fake.NewClientBuilder().
		WithObjects(existingNode).
		WithInterceptorFuncs(f).
		Build()}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}

	if err := r.AddNode(context.Background(), node, nil, nil); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if featureUpdate == nil || featureUpdate.Features == nil || featureUpdate.FeaturesAct == nil {
		t.Fatalf("AddNode() feature update = %#v, want available and active features", featureUpdate)
	}
	want := api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible}
	if !slices.Equal(*featureUpdate.Features, want) {
		t.Errorf("available features = %v, want %v", *featureUpdate.Features, want)
	}
	if !slices.Equal(*featureUpdate.FeaturesAct, want) {
		t.Errorf("active features = %v, want %v", *featureUpdate.FeaturesAct, want)
	}
}

func Test_realSlurmControl_AddNode_updatesExistingHybridNodeInventory(t *testing.T) {
	wantExtra, err := dra.EncodeAppliedInventory(testExampleDRAInventory())
	if err != nil {
		t.Fatalf("EncodeAppliedInventory: %v", err)
	}

	tests := []struct {
		name         string
		currentExtra string
		wantExtra    string
	}{
		{
			name:      "sets missing inventory",
			wantExtra: wantExtra,
		},
		{
			name:         "replaces stale owned inventory",
			currentExtra: `slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":[]}}`,
			wantExtra:    wantExtra,
		},
		{
			name:         "clears removed owned inventory",
			currentExtra: `slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":[]}}`,
			wantExtra:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var updates []api.V0044UpdateNodeMsg
			f := interceptor.Funcs{
				Update: func(_ context.Context, _ object.Object, req any, _ ...slurmclient.UpdateOption) error {
					if update, ok := req.(api.V0044UpdateNodeMsg); ok {
						updates = append(updates, update)
					}
					return nil
				},
			}
			slurmNode := &types.V0044Node{V0044Node: api.V0044Node{
				Name:           ptr.To("test-node"),
				Cpus:           ptr.To(int32(4)),
				RealMemory:     ptr.To(int64(8192)),
				Features:       ptr.To(api.V0044CsvString{wellknown.SlurmFeatureGRESCompatible}),
				ActiveFeatures: ptr.To(api.V0044CsvString{wellknown.SlurmFeatureGRESCompatible}),
			}}
			var inventory []dra.GRESInventory
			if tt.wantExtra != "" {
				slurmNode.Gres = ptr.To("gpu:gpu.example.com:2,nic:infiniband:1")
				inventory = testExampleDRAInventory()
			}
			if tt.currentExtra != "" {
				slurmNode.Extra = ptr.To(tt.currentExtra)
			}
			r := &realSlurmControl{Client: fake.NewClientBuilder().
				WithObjects(slurmNode).
				WithInterceptorFuncs(f).
				Build()}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}

			if err := r.AddNode(context.Background(), node, nil, inventory); err != nil {
				t.Fatalf("AddNode: %v", err)
			}
			if len(updates) != 1 {
				t.Fatalf("AddNode() updates = %d, want 1", len(updates))
			}
			if got := ptr.Deref(updates[0].Extra, "missing"); got != tt.wantExtra {
				t.Errorf("AddNode() Extra = %q, want %q", got, tt.wantExtra)
			}
		})
	}
}

func Test_realSlurmControl_UpdateHybridNode_reconcilesGRESCompatibilityFeature(t *testing.T) {
	tests := []struct {
		name          string
		gres          string
		features      api.V0044CsvString
		active        api.V0044CsvString
		inventory     []dra.GRESInventory
		wantErr       bool
		wantAvailable api.V0044CsvString
		wantActive    api.V0044CsvString
	}{
		{
			name:          "adds feature to compatible node",
			features:      api.V0044CsvString{"admin-feature"},
			active:        api.V0044CsvString{"admin-feature"},
			wantAvailable: api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible},
			wantActive:    api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible},
		},
		{
			name:      "removes feature from incompatible node",
			gres:      "gpu:gpu.example.com:1",
			features:  api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible},
			active:    api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible},
			inventory: testExampleDRAInventory(),
			wantErr:   true,
			// Slurm requires the active feature to be removed before the available feature.
			wantActive:    api.V0044CsvString{"admin-feature"},
			wantAvailable: api.V0044CsvString{"admin-feature"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var updates []api.V0044UpdateNodeMsg
			f := interceptor.Funcs{
				Update: func(_ context.Context, _ object.Object, req any, _ ...slurmclient.UpdateOption) error {
					updates = append(updates, req.(api.V0044UpdateNodeMsg))
					return nil
				},
			}
			slurmNode := &types.V0044Node{V0044Node: api.V0044Node{
				Name:           ptr.To("test-node"),
				Gres:           ptr.To(tt.gres),
				Features:       ptr.To(tt.features),
				ActiveFeatures: ptr.To(tt.active),
			}}
			r := &realSlurmControl{Client: fake.NewClientBuilder().
				WithObjects(slurmNode).
				WithInterceptorFuncs(f).
				Build()}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}

			err := r.UpdateHybridNode(context.Background(), node, tt.inventory)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UpdateHybridNode() error = %v, wantErr %v", err, tt.wantErr)
			}
			if len(updates) != 2 {
				t.Fatalf("UpdateHybridNode() updates = %d, want 2: %#v", len(updates), updates)
			}
			if tt.wantErr {
				if updates[0].FeaturesAct == nil || !slices.Equal(*updates[0].FeaturesAct, tt.wantActive) {
					t.Errorf("first update active features = %v, want %v", updates[0].FeaturesAct, tt.wantActive)
				}
				if updates[1].Features == nil || !slices.Equal(*updates[1].Features, tt.wantAvailable) {
					t.Errorf("second update available features = %v, want %v", updates[1].Features, tt.wantAvailable)
				}
				return
			}
			if updates[0].Features == nil || !slices.Equal(*updates[0].Features, tt.wantAvailable) {
				t.Errorf("first update available features = %v, want %v", updates[0].Features, tt.wantAvailable)
			}
			if updates[1].FeaturesAct == nil || !slices.Equal(*updates[1].FeaturesAct, tt.wantActive) {
				t.Errorf("second update active features = %v, want %v", updates[1].FeaturesAct, tt.wantActive)
			}
		})
	}
}

func Test_realSlurmControl_AddNode_rejectsIncompatibleHybridGRES(t *testing.T) {
	slurmNode := &types.V0044Node{V0044Node: api.V0044Node{
		Name: ptr.To("test-node"),
		Gres: ptr.To("gpu:gpu.example.com:1"),
	}}
	r := &realSlurmControl{Client: fake.NewClientBuilder().WithObjects(slurmNode).Build()}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}

	err := r.AddNode(context.Background(), node, nil, testExampleDRAInventory())
	if err == nil {
		t.Fatal("AddNode() error = nil, want incompatible GRES error")
	}
	var incompatible *IncompatibleGRESConfigurationError
	if !errors.As(err, &incompatible) {
		t.Fatalf("AddNode() error = %T, want *IncompatibleGRESConfigurationError", err)
	}
	if want := "NodeName=test-node Name=gpu Type=gpu.example.com Count=2"; !strings.Contains(err.Error(), want) {
		t.Errorf("AddNode() error = %q, want containing %q", err, want)
	}
}

func Test_realSlurmControl_UpdateHybridNode_doesNotCreateOrModifyExternalNodes(t *testing.T) {
	tests := []struct {
		name      string
		slurmNode *types.V0044Node
		inventory []dra.GRESInventory
	}{
		{
			name:      "absent node",
			inventory: testExampleDRAInventory(),
		},
		{
			name: "external node",
			slurmNode: &types.V0044Node{V0044Node: api.V0044Node{
				Name:  ptr.To("test-node"),
				State: ptr.To([]api.V0044NodeState{api.V0044NodeStateEXTERNAL}),
			}},
			inventory: testExampleDRAInventory(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := false
			f := interceptor.Funcs{
				Create: func(_ context.Context, _ object.Object, _ any, _ ...slurmclient.CreateOption) error {
					mutated = true
					return nil
				},
				Update: func(_ context.Context, _ object.Object, _ any, _ ...slurmclient.UpdateOption) error {
					mutated = true
					return nil
				},
			}
			builder := fake.NewClientBuilder().WithInterceptorFuncs(f)
			if tt.slurmNode != nil {
				builder.WithObjects(tt.slurmNode)
			}
			r := &realSlurmControl{Client: builder.Build()}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}

			if err := r.UpdateHybridNode(context.Background(), node, tt.inventory); err != nil {
				t.Fatalf("UpdateHybridNode: %v", err)
			}
			if mutated {
				t.Fatal("UpdateHybridNode() mutated a node outside hybrid scope")
			}
		})
	}
}

func TestBuildNodeGRESConfigOffsetsProfilesSharingGRESName(t *testing.T) {
	inventory := []dra.GRESInventory{
		{
			GRES:    dra.GRES{Name: "nic", Type: "dranet0"},
			Devices: []dra.DeviceIdentity{structured.MakeDeviceID("dra.net", "node-a", "dranet0")},
		},
		{
			GRES:    dra.GRES{Name: "nic", Type: "sriov-vf"},
			Devices: []dra.DeviceIdentity{structured.MakeDeviceID("sriov.example.com", "node-a", "vf-0")},
		},
	}

	config, err := buildNodeGRESConfig(inventory)
	if err != nil {
		t.Fatalf("buildNodeGRESConfig() error = %v", err)
	}
	applied, err := dra.DecodeAppliedInventory(config.extra)
	if err != nil {
		t.Fatalf("DecodeAppliedInventory() error = %v", err)
	}
	devices, err := applied.Devices("sriov-vf", []int{1})
	if err != nil {
		t.Fatalf("AppliedInventory.Devices() error = %v", err)
	}
	if len(devices) != 1 || devices[0] != inventory[1].Devices[0] {
		t.Fatalf("AppliedInventory.Devices() = %#v, want sriov-vf device at global nic index 1", devices)
	}
}

func Test_realSlurmControl_AddNode_includesTopologyInNodeConfig(t *testing.T) {
	var nodeConf string
	f := interceptor.Funcs{
		Create: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.CreateOption) error {
			if r, ok := req.(api.V0044OpenapiCreateNodeReq); ok {
				nodeConf = r.NodeConf
			}
			return nil
		},
	}
	r := &realSlurmControl{
		Client: fake.NewClientBuilder().WithInterceptorFuncs(f).Build(),
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Annotations: map[string]string{
				wellknown.AnnotationNodeTopologySpec: "topo-switch:s1",
			},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	if err := r.AddNode(context.Background(), node, nil, nil); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if !strings.Contains(nodeConf, "Topology=topo-switch:s1") {
		t.Errorf("NodeConf missing topology: %q", nodeConf)
	}
}

func Test_realSlurmControl_AddNode_updatesExistingNodeTopology(t *testing.T) {
	var gotTopology string
	f := interceptor.Funcs{
		Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
			if r, ok := req.(api.V0044UpdateNodeMsg); ok && r.TopologyStr != nil {
				gotTopology = *r.TopologyStr
			}
			return nil
		},
	}
	existingNode := &types.V0044Node{
		V0044Node: api.V0044Node{
			Name:     ptr.To("test-node"),
			Topology: ptr.To("topo-switch:s1"),
		},
	}
	r := &realSlurmControl{
		Client: fake.NewClientBuilder().WithObjects(existingNode).WithInterceptorFuncs(f).Build(),
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Annotations: map[string]string{
				wellknown.AnnotationNodeTopologySpec: "topo-switch:s2",
			},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	if err := r.AddNode(context.Background(), node, nil, nil); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if gotTopology != "topo-switch:s2" {
		t.Errorf("TopologyStr = %q, want %q", gotTopology, "topo-switch:s2")
	}
}

func Test_realSlurmControl_AddNode_clearsExistingNodeTopology(t *testing.T) {
	gotTopology := "unset"
	f := interceptor.Funcs{
		Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
			if r, ok := req.(api.V0044UpdateNodeMsg); ok && r.TopologyStr != nil {
				gotTopology = *r.TopologyStr
			}
			return nil
		},
	}
	existingNode := &types.V0044Node{
		V0044Node: api.V0044Node{
			Name:     ptr.To("test-node"),
			Topology: ptr.To("topo-switch:s1"),
		},
	}
	r := &realSlurmControl{
		Client: fake.NewClientBuilder().WithObjects(existingNode).WithInterceptorFuncs(f).Build(),
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	if err := r.AddNode(context.Background(), node, nil, nil); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if gotTopology != "" {
		t.Errorf("TopologyStr = %q, want empty string", gotTopology)
	}
}
