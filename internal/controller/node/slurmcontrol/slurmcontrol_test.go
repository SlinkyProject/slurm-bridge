// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"k8s.io/utils/ptr"
	ctrlclientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func init() {
	utilruntime.Must(resourcev1.AddToScheme(scheme.Scheme))
}

func testNodeCPUResourceSlice(nodeName string) *resourcev1.ResourceSlice {
	coreType := ptr.To(int64(nodeinfo.CoreTypeStandard))
	device := func(name string, cpuID, coreID int64) resourcev1.Device {
		return resourcev1.Device{
			Name: name,
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				nodeinfo.DraDriverCpu_CpuID:    {IntValue: ptr.To(cpuID)},
				nodeinfo.DraDriverCpu_CoreID:   {IntValue: ptr.To(coreID)},
				nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To[int64](0)},
				nodeinfo.DraDriverCpu_CoreType: {IntValue: coreType},
			},
		}
	}
	return &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName + "-dra-cpu"},
		Spec: resourcev1.ResourceSliceSpec{
			NodeName: ptr.To(nodeName),
			Driver:   nodeinfo.DraDriverCpu,
			Devices: []resourcev1.Device{
				device("cpu0", 0, 0),
				device("cpu1", 1, 0),
				device("cpu2", 2, 1),
				device("cpu3", 3, 1),
			},
		},
	}
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
		name     string
		client   slurmclient.Client
		node     *corev1.Node
		nodeInfo *nodeinfo.NodeInfo
		want     bool
		wantErr  bool
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
			name: "node exists, different gres (slurm has gres, desired empty)",
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
			want: true,
		},
		{
			name: "node exists, desired gres empty (nodeInfo no GPUs) slurm has gres",
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
			node:     makeNode("worker-0", 4, 8),
			nodeInfo: &nodeinfo.NodeInfo{},
			want:     true,
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
			got, err := r.NodeNeedsRecreate(ctx, tt.node, tt.nodeInfo)
			if (err != nil) != tt.wantErr {
				t.Errorf("NodeNeedsRecreate() error = %v, wantErr %v", err, tt.wantErr)
				return
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

func Test_tolerateError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Nil",
			args: args{
				err: nil,
			},
			want: true,
		},
		{
			name: "Empty",
			args: args{
				err: errors.New(""),
			},
			want: false,
		},
		{
			name: "NotFound",
			args: args{
				err: errors.New(http.StatusText(http.StatusNotFound)),
			},
			want: true,
		},
		{
			name: "NoContent",
			args: args{
				err: errors.New(http.StatusText(http.StatusNoContent)),
			},
			want: true,
		},
		{
			name: "Forbidden",
			args: args{
				err: errors.New(http.StatusText(http.StatusForbidden)),
			},
			want: false,
		},
		{
			name: "wrapped Not Found (e.g. slurm-client cache sync)",
			args: args{
				err: errors.New("failed to wait on type V0044JobInfo object 69 cache sync: [Not Found, Invalid job id specified]"),
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tolerateError(tt.args.err); got != tt.want {
				t.Errorf("tolerateError() = %v, want %v", got, tt.want)
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
			name: "add node with nodeInfo from NewNodeInfo (GRES)",
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
						WithObjects(
							testNodeCPUResourceSlice("test-node"),
							&resourcev1.ResourceSlice{
								ObjectMeta: metav1.ObjectMeta{Name: "test-node-gpu-slice"},
								Spec: resourcev1.ResourceSliceSpec{
									NodeName: ptr.To("test-node"),
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
									},
								},
							},
						).
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
			if err := r.AddNode(tt.args.ctx, tt.args.node, tt.args.nodeInfo); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.AddNode() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_AddNode_withNodeInfo_includesGRESInNodeConfig(t *testing.T) {
	var nodeConf string
	f := interceptor.Funcs{
		Create: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.CreateOption) error {
			if r, ok := req.(api.V0044OpenapiCreateNodeReq); ok {
				nodeConf = r.NodeConf
			}
			return nil
		},
	}
	slurmClient := fake.NewClientBuilder().WithInterceptorFuncs(f).Build()
	kubeClient := ctrlclientfake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(
			testNodeCPUResourceSlice("test-node"),
			&resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node-gpu-slice"},
				Spec: resourcev1.ResourceSliceSpec{
					NodeName: ptr.To("test-node"),
					Driver:   nodeinfo.DraExampleDriver,
					Devices: []resourcev1.Device{
						{
							Name: "gpu-0",
							Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
								nodeinfo.DraExampleDriver_Index: {IntValue: ptr.To[int64](0)},
							},
						},
					},
				},
			},
		).
		Build()
	nodeInfo, err := nodeinfo.NewNodeInfo(context.Background(), kubeClient, "test-node")
	if err != nil {
		t.Fatalf("NewNodeInfo: %v", err)
	}
	r := &realSlurmControl{Client: slurmClient}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	if err := r.AddNode(context.Background(), node, nodeInfo); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if nodeConf == "" {
		t.Fatal("Create was not called or NodeConf was not captured")
	}
	if !strings.Contains(nodeConf, "Gres=") {
		t.Errorf("NodeConf missing Gres=: %q", nodeConf)
	}
	if !strings.Contains(nodeConf, "GresConf=") {
		t.Errorf("NodeConf missing GresConf=: %q", nodeConf)
	}
}
