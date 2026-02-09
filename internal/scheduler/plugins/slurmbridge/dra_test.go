// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2024 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"reflect"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	fwk "k8s.io/kube-scheduler/framework"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	tf "k8s.io/kubernetes/pkg/scheduler/testing/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Test_expandDevices(t *testing.T) {
	type args struct {
		gresCount   int64
		gresIndexes string
	}
	tests := []struct {
		name    string
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "expand indexes",
			args: args{
				gresCount:   3,
				gresIndexes: "1-3",
			},
			want:    []string{"1", "2", "3"},
			wantErr: false,
		},
		{
			name: "expand based on gresCount",
			args: args{
				gresCount:   4,
				gresIndexes: "",
			},
			want:    []string{"0", "1", "2", "3"},
			wantErr: false,
		},
		{
			name: "not a range",
			args: args{
				gresCount:   1,
				gresIndexes: "3",
			},
			want:    []string{"3"},
			wantErr: false,
		},
		{
			name: "bad range",
			args: args{
				gresCount:   3,
				gresIndexes: "1-2-3",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "expand indexes",
			args: args{
				gresCount:   3,
				gresIndexes: "1,3-4",
			},
			want:    []string{"1", "3", "4"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandDevices(tt.args.gresCount, tt.args.gresIndexes)
			if (err != nil) != tt.wantErr {
				t.Errorf("expandDevices() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("expandDevices() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlurmBridge_getAllocationResult(t *testing.T) {
	ctx := context.Background()
	cs := clientsetfake.NewClientset(&resourcev1.DeviceClassList{
		Items: []resourcev1.DeviceClass{
			{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "gpu.example.com"}},
		},
	})
	informerFactory := informers.NewSharedInformerFactory(cs, 0)
	registeredPlugins := []tf.RegisterPluginFunc{
		tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
		tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
	}
	f, err := tf.NewFramework(
		ctx,
		registeredPlugins,
		"slurm-bridge",
		fwkruntime.WithClientSet(cs),
		fwkruntime.WithInformerFactory(informerFactory),
		fwkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(
			[]*corev1.Pod{},
			[]*corev1.Node{},
		)))
	if err != nil {
		t.Fatal(err)
	}
	type fields struct {
		Client        client.Client
		schedulerName string
		slurmControl  slurmcontrol.SlurmControlInterface
		handle        fwk.Handle
	}
	type args struct {
		ctx       context.Context
		nodeName  string
		resources *slurmcontrol.NodeResources
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *resourcev1.AllocationResult
	}{
		{
			name: "No matching device class name",
			fields: fields{
				handle: f,
			},
			args: args{
				ctx:      ctx,
				nodeName: "node1",
				resources: &slurmcontrol.NodeResources{
					Node: "node1",
					Gres: []slurmcontrol.GresLayout{
						{
							Name:  "gpu",
							Type:  "example.com",
							Count: 4,
							Index: "1-4",
						},
					},
				},
			},
			want: &resourcev1.AllocationResult{
				NodeSelector: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchFields: []corev1.NodeSelectorRequirement{
								{
									Key:      "metadata.name",
									Operator: "In",
									Values:   []string{"node1"},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "Matching device class name",
			fields: fields{
				handle: f,
			},
			args: args{
				ctx:      ctx,
				nodeName: "node1",
				resources: &slurmcontrol.NodeResources{
					Node: "node1",
					Gres: []slurmcontrol.GresLayout{
						{
							Name:  "gpu",
							Type:  "gpu.example.com",
							Count: 3,
							Index: "1,3-4",
						},
					},
				},
			},
			want: &resourcev1.AllocationResult{
				Devices: resourcev1.DeviceAllocationResult{
					Results: []resourcev1.DeviceRequestAllocationResult{
						{Request: "gpu", Driver: "gpu.example.com", Pool: "node1", Device: "gpu-1"},
						{Request: "gpu", Driver: "gpu.example.com", Pool: "node1", Device: "gpu-3"},
						{Request: "gpu", Driver: "gpu.example.com", Pool: "node1", Device: "gpu-4"},
					},
				},
				NodeSelector: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchFields: []corev1.NodeSelectorRequirement{
								{
									Key:      "metadata.name",
									Operator: "In",
									Values:   []string{"node1"},
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
			sb := &SlurmBridge{
				Client:        tt.fields.Client,
				schedulerName: tt.fields.schedulerName,
				slurmControl:  tt.fields.slurmControl,
				handle:        tt.fields.handle,
			}
			got := sb.getAllocationResult(tt.args.ctx, tt.args.nodeName, tt.args.resources)
			if !apiequality.Semantic.DeepEqual(got.Devices, tt.want.Devices) {
				t.Errorf("SlurmBridge.getAllocationResult() got.Devices = %v, want %v", got.Devices, tt.want.Devices)
			}
			if !apiequality.Semantic.DeepEqual(got.NodeSelector, tt.want.NodeSelector) {
				t.Errorf("SlurmBridge.getAllocationResult() got.NodeSelector = %v, want %v", got.NodeSelector, tt.want.NodeSelector)
			}
		})
	}
}
