// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/testutils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func newQueue() workqueue.TypedRateLimitingInterface[reconcile.Request] {
	return workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
}

func Test_nodeEventHandler_Create(t *testing.T) {
	type fields struct {
		Reader client.Reader
	}
	type args struct {
		ctx context.Context
		evt event.CreateEvent
		q   workqueue.TypedRateLimitingInterface[reconcile.Request]
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "Empty",
			fields: fields{
				Reader: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				evt: event.CreateEvent{},
				q:   newQueue(),
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &nodeEventHandler{
				Reader: tt.fields.Reader,
			}
			h.Create(tt.args.ctx, tt.args.evt, tt.args.q)
			if got := tt.args.q.Len(); got > tt.want {
				t.Errorf("Create() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_nodeEventHandler_Delete(t *testing.T) {
	type fields struct {
		Reader client.Reader
	}
	type args struct {
		ctx context.Context
		evt event.DeleteEvent
		q   workqueue.TypedRateLimitingInterface[reconcile.Request]
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "Empty",
			fields: fields{
				Reader: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				evt: event.DeleteEvent{},
				q:   newQueue(),
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &nodeEventHandler{
				Reader: tt.fields.Reader,
			}
			h.Delete(tt.args.ctx, tt.args.evt, tt.args.q)
			if got := tt.args.q.Len(); got > tt.want {
				t.Errorf("Delete() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_nodeEventHandler_Generic(t *testing.T) {
	type fields struct {
		Reader client.Reader
	}
	type args struct {
		ctx context.Context
		evt event.GenericEvent
		q   workqueue.TypedRateLimitingInterface[reconcile.Request]
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "Empty",
			fields: fields{
				Reader: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				evt: event.GenericEvent{},
				q:   newQueue(),
			},
			want: 0,
		},
		{
			name: "Populated",
			fields: fields{
				Reader: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				evt: event.GenericEvent{
					Object: &corev1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-0",
						},
					},
				},
				q: newQueue(),
			},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &nodeEventHandler{
				Reader: tt.fields.Reader,
			}
			h.Generic(tt.args.ctx, tt.args.evt, tt.args.q)
			if got := tt.args.q.Len(); got > tt.want {
				t.Errorf("Generic() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_nodeEventHandler_Update(t *testing.T) {
	type fields struct {
		Reader client.Reader
	}
	type args struct {
		ctx context.Context
		evt event.UpdateEvent
		q   workqueue.TypedRateLimitingInterface[reconcile.Request]
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   int
	}{
		{
			name: "Empty",
			fields: fields{
				Reader: fake.NewFakeClient(),
			},
			args: args{
				ctx: context.TODO(),
				evt: event.UpdateEvent{},
				q:   newQueue(),
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &nodeEventHandler{
				Reader: tt.fields.Reader,
			}
			h.Update(tt.args.ctx, tt.args.evt, tt.args.q)
			if got := tt.args.q.Len(); got > tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_resourceSliceToNodes(t *testing.T) {
	externalNode := func(name string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{wellknown.LabelExternalNode: ""},
		}}
	}
	r := &NodeReconciler{
		Client: fake.NewClientBuilder().WithObjects(
			externalNode("node-a"),
			externalNode("node-b"),
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-c"}},
		).Build(),
		draRegistry: testutils.DRARegistryWithExampleGPU(),
	}

	tests := []struct {
		name  string
		slice *resourcev1.ResourceSlice
		want  []string
	}{
		{
			name: "node-local example slice",
			slice: &resourcev1.ResourceSlice{Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.example.com",
				NodeName: ptr.To("node-a"),
				Devices:  []resourcev1.Device{{Name: "gpu-0"}},
			}},
			want: []string{"node-a"},
		},
		{
			name: "node-local NVIDIA GPU slice",
			slice: &resourcev1.ResourceSlice{Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.nvidia.com",
				NodeName: ptr.To("node-a"),
				Devices: []resourcev1.Device{{
					Name: "gpu-0",
					Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						"type": {StringValue: ptr.To("gpu")},
					},
				}},
			}},
			want: []string{"node-a"},
		},
		{
			name: "unsupported driver",
			slice: &resourcev1.ResourceSlice{Spec: resourcev1.ResourceSliceSpec{
				Driver:   "unsupported.example.com",
				NodeName: ptr.To("node-a"),
				Devices:  []resourcev1.Device{{Name: "device-0"}},
			}},
		},
		{
			name: "all nodes reconcile unsupported allNodes selection",
			slice: &resourcev1.ResourceSlice{Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.example.com",
				AllNodes: ptr.To(true),
				Devices:  []resourcev1.Device{{Name: "gpu-0"}},
			}},
			want: []string{"node-a", "node-b", "node-c"},
		},
		{
			name: "all nodes reconcile unsupported per-device selection",
			slice: &resourcev1.ResourceSlice{Spec: resourcev1.ResourceSliceSpec{
				Driver:                 "gpu.example.com",
				PerDeviceNodeSelection: ptr.To(true),
				Devices: []resourcev1.Device{
					{Name: "gpu-a", NodeName: ptr.To("node-a")},
					{Name: "gpu-c", NodeName: ptr.To("node-c")},
				},
			}},
			want: []string{"node-a", "node-b", "node-c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := r.resourceSliceToNodes(context.Background(), tt.slice)
			got := make([]string, len(requests))
			for i, request := range requests {
				got[i] = request.Name
			}
			sort.Strings(got)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("resourceSliceToNodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResourceSliceToNodesReconcilesWholePool(t *testing.T) {
	local := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "local"},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: "gpu.example.com", NodeName: ptr.To("node-a"),
			Pool: resourcev1.ResourcePool{Name: "shared-pool", Generation: 1, ResourceSliceCount: 1},
		},
	}
	otherDriver := local.DeepCopy()
	otherDriver.Name = "other-driver"
	otherDriver.Spec.Driver = "dra.cpu"
	otherDriver.Spec.NodeName = ptr.To("node-c")
	for _, indexed := range []bool{true, false} {
		builder := fake.NewClientBuilder().WithObjects(local, otherDriver)
		if indexed {
			builder.WithIndex(&resourcev1.ResourceSlice{}, nodeutils.IndexFieldResourceSlicePool, nodeutils.IndexResourceSliceByPool)
		}
		r := &NodeReconciler{Client: builder.Build(), draRegistry: testutils.DRARegistryWithExampleGPU()}
		// A changed or deleted slice may not be in the cache. Its node and the
		// nodes of every other generation in the pool still need reconciliation.
		changed := local.DeepCopy()
		changed.Name = "changed"
		changed.Spec.NodeName = ptr.To("node-b")
		changed.Spec.Pool.Generation = 2
		requests := r.resourceSliceToNodes(context.Background(), changed)
		var got []string
		for _, request := range requests {
			got = append(got, request.Name)
		}
		if want := []string{"node-a", "node-b"}; !slices.Equal(got, want) {
			t.Fatalf("resourceSliceToNodes() = %v, want %v (indexed=%t)", got, want, indexed)
		}
	}
}

func TestNodeRegistrationInventoriesRejectsConflictingPoolNodes(t *testing.T) {
	local := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "local"},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: "gpu.example.com", NodeName: ptr.To("node-a"),
			Pool:    resourcev1.ResourcePool{Name: "shared-pool", Generation: 1, ResourceSliceCount: 1},
			Devices: []resourcev1.Device{{Name: "gpu-a"}},
		},
	}
	remote := local.DeepCopy()
	remote.Name = "remote"
	remote.Spec.NodeName = ptr.To("node-b")
	remote.Spec.Devices[0].Name = "gpu-b"
	r := &NodeReconciler{
		Client: fake.NewClientBuilder().
			WithIndex(&resourcev1.ResourceSlice{}, nodeutils.IndexFieldResourceSliceNode, nodeutils.IndexResourceSliceByNode).
			WithIndex(&resourcev1.ResourceSlice{}, nodeutils.IndexFieldResourceSlicePool, nodeutils.IndexResourceSliceByPool).
			WithObjects(local, remote).Build(),
		draRegistry: testutils.DRARegistryWithExampleGPU(),
	}
	for _, name := range []string{"node-a", "node-b"} {
		_, _, err := r.nodeRegistrationInventories(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
		if err == nil || !strings.Contains(err.Error(), "inconsistent nodeName") {
			t.Fatalf("nodeRegistrationInventories(%q) error = %v, want inconsistent nodeName", name, err)
		}
	}
}

func TestNodeRegistrationInventoriesPrefersDeviceProfiles(t *testing.T) {
	tests := []struct {
		name        string
		driver      string
		device      resourcev1.Device
		wantProfile string
	}{
		{
			name:   "example GPU",
			driver: "gpu.example.com",
			device: resourcev1.Device{
				Name: "gpu-0",
				Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
					"index": {IntValue: ptr.To[int64](0)},
				},
			},
			wantProfile: "gpu-example",
		},
		{
			name:   "NVIDIA GPU",
			driver: "gpu.nvidia.com",
			device: resourcev1.Device{
				Name: "gpu-0",
				Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
					"type": {StringValue: ptr.To("gpu")},
				},
			},
			wantProfile: "gpu-nvidia",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
			resourceSlice := &resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "node-a-gpus"},
				Spec: resourcev1.ResourceSliceSpec{
					Driver:   tt.driver,
					NodeName: ptr.To(node.Name),
					Pool: resourcev1.ResourcePool{
						Name:               node.Name,
						Generation:         1,
						ResourceSliceCount: 1,
					},
					Devices: []resourcev1.Device{tt.device},
				},
			}
			r := &NodeReconciler{
				Client:      fake.NewClientBuilder().WithObjects(node, resourceSlice).Build(),
				draRegistry: testutils.DRARegistryWithExampleGPU(),
			}

			_, inventory, err := r.nodeRegistrationInventories(context.Background(), node)
			if err != nil {
				t.Fatalf("nodeRegistrationInventories() error = %v", err)
			}
			if len(inventory) != 1 || inventory[0].GRES != (dra.GRES{Name: "gpu", Type: tt.wantProfile}) {
				t.Fatalf("nodeRegistrationInventories() profile inventory = %#v, want gpu:%s", inventory, tt.wantProfile)
			}
		})
	}
}

func Test_enqueueNode(t *testing.T) {
	type args struct {
		q    workqueue.TypedRateLimitingInterface[reconcile.Request]
		node *corev1.Node
	}
	tests := []struct {
		name    string
		args    args
		enqueue bool
	}{
		{
			name: "do nothing",
			args: args{
				q:    newQueue(),
				node: nil,
			},
			enqueue: false,
		},
		{
			name: "enqueue node",
			args: args{
				q: newQueue(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-0",
					},
				},
			},
			enqueue: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueueNode(tt.args.q, tt.args.node)
			if tt.args.q.Len() > 0 != tt.enqueue {
				t.Errorf("enqueueNode() have = %v, enqueue %v", tt.args.q.Len(), tt.enqueue)
			}
		})
	}
}

func Test_enqueueNodeAfter(t *testing.T) {
	type args struct {
		q        workqueue.TypedRateLimitingInterface[reconcile.Request]
		node     *corev1.Node
		duration time.Duration
	}
	tests := []struct {
		name    string
		args    args
		enqueue bool
	}{
		{
			name: "do nothing",
			args: args{q: newQueue(),
				node: nil,
			},
			enqueue: false,
		},
		{
			name: "enqueue node",
			args: args{
				q: newQueue(),
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-0",
					},
				},
			},
			enqueue: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enqueueNodeAfter(tt.args.q, tt.args.node, tt.args.duration)
			if tt.args.q.Len() > 0 != tt.enqueue {
				t.Errorf("enqueueNode() have = %v, enqueue %v", tt.args.q.Len(), tt.enqueue)
			}
		})
	}
}
