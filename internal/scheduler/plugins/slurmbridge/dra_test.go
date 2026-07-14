// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2024 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	fwk "k8s.io/kube-scheduler/framework"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	tf "k8s.io/kubernetes/pkg/scheduler/testing/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func TestSlurmBridge_createRequestsAndMappings(t *testing.T) {
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
		pod       *corev1.Pod
		nodeName  string
		resources *slurmcontrol.NodeResources
	}
	tests := []struct {
		name           string
		fields         fields
		args           args
		wantErr        bool
		wantClaim      bool
		wantRequests   int
		wantCPUMapping bool
	}{
		{
			name: "No matching device class name",
			fields: fields{
				Client: fake.NewClientBuilder().
					WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
					Build(),
				handle: f,
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "foo",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("4"),
										corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("4"),
										corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
									},
								},
							},
						},
					},
				},
				nodeName: "node1",
				resources: &slurmcontrol.NodeResources{
					Node:       "node1",
					CoreBitmap: bitmaputil.String(bitmaputil.New(0, 1)),
					Gres: []slurmcontrol.GresLayout{
						{
							Name:  "gpu",
							Type:  "example.com",
							Count: 4,
							Index: "0-3",
						},
					},
				},
			},
		},
		{
			name: "CPU DRA request without CPU DeviceClass",
			fields: fields{
				Client: fake.NewClientBuilder().
					WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
					Build(),
				handle: f,
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "foo",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceName(nodeinfo.DraDriverCpu_ExtendedResourceName): resource.MustParse("2"),
									},
								},
							},
						},
					},
				},
				nodeName: "node1",
				resources: &slurmcontrol.NodeResources{
					Node:       "node1",
					CoreBitmap: bitmaputil.String(bitmaputil.New(0)),
				},
			},
			wantErr: true,
		},
		{
			name: "Partial gres type does not map to device class resource",
			fields: fields{
				handle: f,
				Client: fake.NewClientBuilder().
					WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
					WithObjects(
						&resourcev1.DeviceClass{
							ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraExampleDriver},
						},
					).
					Build(),
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "foo",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "foo",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("1"),
								},
							},
						}},
					},
				},
				nodeName: "node1",
				resources: &slurmcontrol.NodeResources{
					Gres: []slurmcontrol.GresLayout{{
						Name:  "gpu",
						Type:  "example.com",
						Count: 1,
						Index: "0",
					}},
				},
			},
		},
		{
			name: "Matching device class name",
			fields: fields{
				handle: f,
				Client: fake.NewClientBuilder().
					WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
					WithObjects(
						&corev1.Node{
							ObjectMeta: metav1.ObjectMeta{Name: "node1"},
						}, &resourcev1.DeviceClass{
							ObjectMeta: metav1.ObjectMeta{
								Name: nodeinfo.DraDriverCpu,
							},
						},
						&resourcev1.ResourceSlice{
							ObjectMeta: metav1.ObjectMeta{
								Name: "node1-cpu",
							},
							Spec: resourcev1.ResourceSliceSpec{
								NodeName: ptr.To("node1"),
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
						&resourcev1.DeviceClass{
							ObjectMeta: metav1.ObjectMeta{
								Name: nodeinfo.DraExampleDriver,
							},
						},
						&resourcev1.ResourceSlice{
							ObjectMeta: metav1.ObjectMeta{
								Name: "node1-gpu",
							},
							Spec: resourcev1.ResourceSliceSpec{
								NodeName: ptr.To("node1"),
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
			},
			args: args{
				ctx: ctx,
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "foo",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "foo",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceName(nodeinfo.DraDriverCpu_ExtendedResourceName):           resource.MustParse("4"),
										corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceName(nodeinfo.DraDriverCpu_ExtendedResourceName):           resource.MustParse("4"),
										corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
									},
								},
							},
						},
					},
				},
				nodeName: "node1",
				resources: &slurmcontrol.NodeResources{
					Node:       "node1",
					CoreBitmap: bitmaputil.String(bitmaputil.New(0, 1)),
					Gres: []slurmcontrol.GresLayout{
						{
							Name:  "gpu",
							Type:  "gpu.example.com",
							Count: 3,
							Index: "0,2-3",
						},
					},
				},
			},
			wantClaim:      true,
			wantRequests:   2,
			wantCPUMapping: true,
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
			gotClaim, gotMappings, err := sb.createRequestsAndMappings(tt.args.ctx, tt.args.pod, tt.args.nodeName, tt.args.resources)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantClaim {
				if gotClaim != nil || gotMappings != nil {
					t.Errorf("SlurmBridge.createRequestsAndMappings() = (%v, %v), want nil claim and mappings", gotClaim, gotMappings)
				}
				if tt.name == "Partial gres type does not map to device class resource" {
					for _, m := range gotMappings {
						if strings.HasPrefix(m.ResourceName, resourcev1.ResourceDeviceClassPrefix) {
							t.Fatalf("unexpected device class mapping %q", m.ResourceName)
						}
					}
				}
				return
			}
			if gotClaim == nil {
				t.Fatal("SlurmBridge.createRequestsAndMappings() claim = nil, want non-nil")
				return
			}
			if len(gotClaim.Spec.Devices.Requests) != tt.wantRequests {
				t.Errorf("SlurmBridge.createRequestsAndMappings() len(gotClaim.Spec.Devices.Requests) = %v, want %v", len(gotClaim.Spec.Devices.Requests), tt.wantRequests)
			}
			if tt.wantCPUMapping && !hasContainerExtendedResourceRequest(gotMappings, corev1.ContainerExtendedResourceRequest{
				ContainerName: "foo",
				RequestName:   corev1.ResourceCPU.String(),
				ResourceName:  nodeinfo.DraDriverCpu_ExtendedResourceName,
			}) {
				t.Errorf("SlurmBridge.createRequestsAndMappings() mappings = %v, want CPU extended resource mapping", gotMappings)
			}
			if tt.name == "Matching device class name" {
				want := corev1.ContainerExtendedResourceRequest{
					ContainerName: "foo",
					RequestName:   "gpu",
					ResourceName:  resourcev1.ResourceDeviceClassPrefix + nodeinfo.DraExampleDriver,
				}
				if !hasContainerExtendedResourceRequest(gotMappings, want) {
					t.Errorf("mappings = %v, want %v", gotMappings, want)
				}
			}
		})
	}
}

func hasContainerExtendedResourceRequest(mappings []corev1.ContainerExtendedResourceRequest, want corev1.ContainerExtendedResourceRequest) bool {
	for _, mapping := range mappings {
		if mapping == want {
			return true
		}
	}
	return false
}

func TestSlurmBridge_manageResourceClaim_deletesClaimOnError(t *testing.T) {
	ctx := context.Background()
	injectedErr := errors.New("injected client error")

	newPod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: metav1.NamespaceDefault,
				Name:      "foo",
				UID:       "123",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name: "foo",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("1"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("1"),
							},
						},
					},
				},
			},
		}
	}

	resources := &slurmcontrol.NodeResources{
		Node: "node1",
		Gres: []slurmcontrol.GresLayout{
			{
				Name:  "gpu",
				Type:  nodeinfo.DraExampleDriver,
				Count: 1,
				Index: "0",
			},
		},
	}
	newClient := func(pod *corev1.Pod, funcs interceptor.Funcs) client.Client {
		return fake.NewClientBuilder().
			WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
			WithObjects(
				pod,
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
				},
				&resourcev1.DeviceClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeinfo.DraExampleDriver,
					},
				},
				&resourcev1.ResourceSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1-gpu",
					},
					Spec: resourcev1.ResourceSliceSpec{
						NodeName: ptr.To("node1"),
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
			WithStatusSubresource(
				pod,
				&resourcev1.ResourceClaim{},
			).
			WithInterceptorFuncs(funcs).
			Build()
	}

	tests := []struct {
		name  string
		funcs interceptor.Funcs
	}{
		{
			name: "create claim failure",
			funcs: interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*resourcev1.ResourceClaim); ok {
						if err := c.Create(ctx, obj, opts...); err != nil {
							return err
						}
						return injectedErr
					}
					return c.Create(ctx, obj, opts...)
				},
			},
		},
		{
			name: "bind claim failure",
			funcs: interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if subResourceName == "status" {
						if _, ok := obj.(*resourcev1.ResourceClaim); ok {
							return injectedErr
						}
					}
					return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
				},
			},
		},
		{
			name: "pod status failure",
			funcs: interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if subResourceName == "status" {
						if _, ok := obj.(*corev1.Pod); ok {
							return injectedErr
						}
					}
					return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := newPod()
			kclient := newClient(pod, tt.funcs)
			sb := &SlurmBridge{
				Client: kclient,
			}

			gotErr := sb.manageResourceClaim(ctx, pod, resources.Node, resources)
			if gotErr == nil {
				t.Fatal("SlurmBridge.manageResourceClaim() error = nil, want error")
			}

			claimList := &resourcev1.ResourceClaimList{}
			if err := kclient.List(ctx, claimList); err != nil {
				t.Fatalf("Client.List(ResourceClaimList) error = %v, want nil", err)
			}
			if len(claimList.Items) != 0 {
				t.Fatalf("Client.List(ResourceClaimList) got %d claims, want 0", len(claimList.Items))
			}
		})
	}
}

func TestValidateDeviceClassRequestsRejectsMultipleContainers(t *testing.T) {
	gpuResource := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + nodeinfo.DraDriverGpuNvidia)
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{
			Name: "first",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				gpuResource: resource.MustParse("1"),
			}},
		},
		{
			Name: "second",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				gpuResource: resource.MustParse("1"),
			}},
		},
	}}}
	if err := validateDeviceClassRequests(pod); err == nil || !strings.Contains(err.Error(), "requested by multiple containers") {
		t.Fatalf("validateDeviceClassRequests() error = %v, want multiple containers error", err)
	}
}

func TestSlurmBridge_manageResourceClaimKeepsGPURequestNamesConsistent(t *testing.T) {
	ctx := context.Background()
	gpuResource := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + nodeinfo.DraDriverGpuNvidia)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      "gpu-test",
			UID:       "123",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "test",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					gpuResource:           resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				}},
			}},
		},
	}
	resources := &slurmcontrol.NodeResources{
		Node: "node1",
		Gres: []slurmcontrol.GresLayout{{
			Name:  "gpu",
			Type:  nodeinfo.DraDriverGpuNvidia,
			Count: 1,
			Index: "0",
		}},
	}
	kclient := fake.NewClientBuilder().
		WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
		WithObjects(
			pod,
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
			&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraDriverGpuNvidia}},
			&resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "node1-gpu"},
				Spec: resourcev1.ResourceSliceSpec{
					NodeName: ptr.To("node1"),
					Pool:     resourcev1.ResourcePool{Name: "node1"},
					Driver:   nodeinfo.DraDriverGpuNvidia,
					Devices:  []resourcev1.Device{{Name: "gpu-0"}},
				},
			},
		).
		WithStatusSubresource(pod, &resourcev1.ResourceClaim{}).
		Build()
	sb := &SlurmBridge{Client: kclient}

	if err := sb.manageResourceClaim(ctx, pod, resources.Node, resources); err != nil {
		t.Fatalf("manageResourceClaim() error = %v", err)
	}

	claimList := &resourcev1.ResourceClaimList{}
	if err := kclient.List(ctx, claimList, client.InNamespace(pod.Namespace)); err != nil {
		t.Fatalf("list ResourceClaims: %v", err)
	}
	if len(claimList.Items) != 1 {
		t.Fatalf("ResourceClaims = %d, want 1", len(claimList.Items))
	}
	claim := claimList.Items[0]
	updatedPod := &corev1.Pod{}
	if err := kclient.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod); err != nil {
		t.Fatalf("get Pod: %v", err)
	}
	if updatedPod.Status.ExtendedResourceClaimStatus == nil {
		t.Fatal("pod extendedResourceClaimStatus = nil")
	}

	wantRequestName := "gpu"
	mappings := updatedPod.Status.ExtendedResourceClaimStatus.RequestMappings
	if len(mappings) != 1 || mappings[0].RequestName != wantRequestName {
		t.Fatalf("pod request mappings = %#v, want request name %q", mappings, wantRequestName)
	}
	if len(claim.Spec.Devices.Requests) != 1 || claim.Spec.Devices.Requests[0].Name != wantRequestName {
		t.Fatalf("claim device requests = %#v, want request name %q", claim.Spec.Devices.Requests, wantRequestName)
	}
	results := claim.Status.Allocation.Devices.Results
	if len(results) != 1 || results[0].Request != wantRequestName {
		t.Fatalf("claim allocation results = %#v, want request name %q", results, wantRequestName)
	}
}

func TestSlurmBridge_bindClaim(t *testing.T) {
	tests := []struct {
		name      string
		kclient   client.Client
		claim     *resourcev1.ResourceClaim
		pod       *corev1.Pod
		nodeName  string
		resources *slurmcontrol.NodeResources
		wantErr   bool
	}{
		{
			name: "smoke",
			kclient: fake.NewClientBuilder().
				WithIndex(&resourcev1.ResourceSlice{}, "spec.nodeName", resourceSliceNodeIndex).
				WithObjects(
					&corev1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node1",
						},
					},
					&resourcev1.DeviceClass{
						ObjectMeta: metav1.ObjectMeta{
							Name: nodeinfo.DraDriverCpu,
						},
					},
					&resourcev1.ResourceSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node1-cpu",
						},
						Spec: resourcev1.ResourceSliceSpec{
							NodeName: ptr.To("node1"),
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
					&resourcev1.DeviceClass{
						ObjectMeta: metav1.ObjectMeta{
							Name: nodeinfo.DraExampleDriver,
						},
					},
					&resourcev1.ResourceSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node1-gpu",
						},
						Spec: resourcev1.ResourceSliceSpec{
							NodeName: ptr.To("node1"),
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
					&resourcev1.ResourceClaim{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: metav1.NamespaceDefault,
							Name:      "claim1",
						},
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "cpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: nodeinfo.DraDriverCpu,
											Count:           4,
											Selectors: []resourcev1.DeviceSelector{
												{
													CEL: &resourcev1.CELDeviceSelector{
														Expression: "device.attributes['dra.cpu'].cpuID in [0,1,2,3]",
													},
												},
											},
										},
									},
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: nodeinfo.DraExampleDriver,
											Count:           3,
											Selectors: []resourcev1.DeviceSelector{
												{
													CEL: &resourcev1.CELDeviceSelector{
														Expression: "device.attributes['gpu.example.com'].index in [1,3,4]",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					&corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: metav1.NamespaceDefault,
							Name:      "foo",
							UID:       "123",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "foo",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("4"),
											corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("4"),
											corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
										},
									},
								},
							},
						},
					},
				).
				WithStatusSubresource(
					&resourcev1.ResourceClaim{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: metav1.NamespaceDefault,
							Name:      "claim1",
						},
						Spec: resourcev1.ResourceClaimSpec{
							Devices: resourcev1.DeviceClaim{
								Requests: []resourcev1.DeviceRequest{
									{
										Name: "gpu",
										Exactly: &resourcev1.ExactDeviceRequest{
											DeviceClassName: nodeinfo.DraExampleDriver,
											Count:           3,
											Selectors: []resourcev1.DeviceSelector{
												{
													CEL: &resourcev1.CELDeviceSelector{
														Expression: "device.attributes['gpu.example.com'].index in [1,3,4]",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					&corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: metav1.NamespaceDefault,
							Name:      "foo",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "foo",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("4"),
											corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("4"),
											corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
										},
									},
								},
							},
						},
					},
				).
				Build(),
			claim: &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: metav1.NamespaceDefault,
					Name:      "claim1",
				},
				Spec: resourcev1.ResourceClaimSpec{
					Devices: resourcev1.DeviceClaim{
						Requests: []resourcev1.DeviceRequest{
							{
								Name: "cpu",
								Exactly: &resourcev1.ExactDeviceRequest{
									DeviceClassName: nodeinfo.DraDriverCpu,
									Count:           4,
									Selectors: []resourcev1.DeviceSelector{
										{
											CEL: &resourcev1.CELDeviceSelector{
												Expression: "device.attributes['dra.cpu'].cpuID in [0,1,2,3]",
											},
										},
									},
								},
							},
							{
								Name: "gpu",
								Exactly: &resourcev1.ExactDeviceRequest{
									DeviceClassName: nodeinfo.DraExampleDriver,
									Count:           3,
									Selectors: []resourcev1.DeviceSelector{
										{
											CEL: &resourcev1.CELDeviceSelector{
												Expression: "device.attributes['gpu.example.com'].index in [1,3,4]",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: metav1.NamespaceDefault,
					Name:      "foo",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "foo",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("4"),
									corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("4"),
									corev1.ResourceName("deviceclass.resource.kubernetes.io/gpu.example.com"): resource.MustParse("3"),
								},
							},
						},
					},
				},
			},
			nodeName: "node1",
			resources: &slurmcontrol.NodeResources{
				Node: "node1",
				Gres: []slurmcontrol.GresLayout{
					{
						Name:  "gpu",
						Type:  "example.com",
						Count: 3,
						Index: "0,2-3",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client: tt.kclient,
			}
			gotErr := sb.bindClaim(context.Background(), tt.claim, tt.pod, tt.nodeName, tt.resources)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("bindClaim() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("bindClaim() succeeded unexpectedly")
			}
		})
	}
}

func TestSlurmBridge_patchPodExtendedResourceClaimStatus(t *testing.T) {
	tests := []struct {
		name            string
		kclient         client.Client
		pod             *corev1.Pod
		claim           *resourcev1.ResourceClaim
		requestMappings []corev1.ContainerExtendedResourceRequest
		wantErr         bool
	}{
		{
			name:    "empty",
			kclient: fake.NewFakeClient(),
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: corev1.NamespaceDefault,
					Name:      "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "smoke",
			kclient: fake.NewClientBuilder().
				WithObjects(
					&corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: corev1.NamespaceDefault,
							Name:      "foo",
						},
					},
				).
				WithStatusSubresource(
					&corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: corev1.NamespaceDefault,
							Name:      "foo",
						},
					},
				).
				Build(),
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: corev1.NamespaceDefault,
					Name:      "foo",
				},
			},
			claim: &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: metav1.NamespaceDefault,
					Name:      "claim1",
				},
				Spec: resourcev1.ResourceClaimSpec{
					Devices: resourcev1.DeviceClaim{
						Requests: []resourcev1.DeviceRequest{
							{
								Name: "gpu",
								Exactly: &resourcev1.ExactDeviceRequest{
									DeviceClassName: nodeinfo.DraExampleDriver,
									Count:           3,
									Selectors: []resourcev1.DeviceSelector{
										{
											CEL: &resourcev1.CELDeviceSelector{
												Expression: "device.attributes['gpu.example.com'].index in [1,3,4]",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			requestMappings: []corev1.ContainerExtendedResourceRequest{
				{
					ContainerName: "foo",
					ResourceName:  "cpu",
					RequestName:   "container-0-request-0",
				},
				{
					ContainerName: "foo",
					ResourceName:  "deviceclass.resource.kubernetes.io/gpu.example.com",
					RequestName:   "container-0-request-1",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client: tt.kclient,
			}
			gotErr := sb.patchPodExtendedResourceClaimStatus(context.Background(), tt.pod, tt.claim, tt.requestMappings)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("patchPodExtendedResourceClaimStatus() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("patchPodExtendedResourceClaimStatus() succeeded unexpectedly")
			}
		})
	}
}
