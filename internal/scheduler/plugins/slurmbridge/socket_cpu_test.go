// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	coreapi "k8s.io/kubernetes/pkg/apis/core"
	coreinstall "k8s.io/kubernetes/pkg/apis/core/install"
	corevalidation "k8s.io/kubernetes/pkg/apis/core/validation"
	resourceapi "k8s.io/kubernetes/pkg/apis/resource"
	resourceinstall "k8s.io/kubernetes/pkg/apis/resource/install"
	resourcevalidation "k8s.io/kubernetes/pkg/apis/resource/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/testutils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func socketClaimFixture(t *testing.T, funcs interceptor.Funcs) (*SlurmBridge, *corev1.Pod, *slurmcontrol.NodeResources) {
	t.Helper()
	slice := &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: "cpu-pool"}, Spec: resourcev1.ResourceSliceSpec{Driver: nodeinfo.DraDriverCpu, NodeName: ptr.To("node"), Pool: resourcev1.ResourcePool{Name: "node", Generation: 12, ResourceSliceCount: 1}}}
	for socket := 0; socket < 2; socket++ {
		slice.Spec.Devices = append(slice.Spec.Devices, resourcev1.Device{Name: fmt.Sprintf("socket-%d", socket), AllowMultipleAllocations: ptr.To(true), Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			nodeinfo.DraDriverCpu_SocketID: {IntValue: ptr.To(int64(socket))}, nodeinfo.DraDriverCpu_CPUIDs: {StringValue: ptr.To(fmt.Sprintf("%d-%d", socket*64, socket*64+63))}, nodeinfo.DraDriverCpu_SMTLayout: {StringValue: ptr.To("0-127>32")}, nodeinfo.DraDriverCpu_SMTEnabled: {BoolValue: ptr.To(true)},
		}, Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{nodeinfo.DraDriverCpu_Capacity: {Value: resource.MustParse("64")}}})
	}
	topology, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{*slice})
	if err != nil {
		t.Fatal(err)
	}
	extra, err := topology.EncodeApplied(`slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":{"firstIndex":0,"devices":["/dra/gpu.example.com/node/gpu0"]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	cores := make([]int, 64)
	for i := range cores {
		cores[i] = i
	}
	resources := &slurmcontrol.NodeResources{Node: "node", NodeExtra: extra, CoreBitmap: bitmaputil.String(bitmaputil.New(cores...)), SocketsPerNode: 2, CoresPerSocket: 32, Gres: []slurmcontrol.GresLayout{{Name: "gpu", Type: "gpu.example.com", Count: 1, Index: "0"}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "default", UID: "pod-uid", ResourceVersion: "1", Labels: map[string]string{wellknown.LabelExternalJobId: "17"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "pause", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "cpus"): resource.MustParse("128"), corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "gpus"): resource.MustParse("1")}}}}}}
	class := func(name, driver string) *resourcev1.DeviceClass {
		return &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: fmt.Sprintf(`device.driver == %q`, driver)}}}}}
	}
	kclient := fake.NewClientBuilder().WithObjects(slice, pod, class("cpus", nodeinfo.DraDriverCpu), func() *resourcev1.DeviceClass {
		c := class("gpus", "gpu.example.com")
		p, _ := testutils.DRARegistryWithExampleGPU().LookupByName("gpu.example.com")
		c.Spec.Selectors[0].CEL.Expression = p.Selector
		return c
	}()).WithStatusSubresource(&resourcev1.ResourceClaim{}, &corev1.Pod{}).WithInterceptorFuncs(funcs).Build()
	return &SlurmBridge{Client: kclient, draRegistry: testutils.DRARegistryWithExampleGPU()}, pod, resources
}

func TestSocketClaim128ThreadsWithGPUAndAPIValidation(t *testing.T) {
	sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{})
	ctx := context.Background()
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err != nil {
		t.Fatal(err)
	}
	claim := &resourcev1.ResourceClaim{}
	if err := sb.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: allocationClaimName(pod)}, claim); err != nil {
		t.Fatal(err)
	}
	if len(claim.Spec.Devices.Requests) != 3 || len(claim.Status.Allocation.Devices.Results) != 3 {
		t.Fatal("128 threads must produce two CPU requests/results alongside one GPU")
	}
	for _, config := range claim.Status.Allocation.Devices.Config {
		if config.Source != resourcev1.AllocationConfigSourceClaim || len(config.Requests) != 1 {
			t.Fatalf("unscoped config %+v", config)
		}
	}
	scheme := runtime.NewScheme()
	resourceinstall.Install(scheme)
	coreinstall.Install(scheme)
	internal := &resourceapi.ResourceClaim{}
	if err := scheme.Convert(claim, internal, nil); err != nil {
		t.Fatal(err)
	}
	if errs := resourcevalidation.ValidateResourceClaim(internal); len(errs) > 0 {
		t.Fatalf("API rejects claim: %v", errs)
	}
	previous := internal.DeepCopy()
	previous.Status = resourceapi.ResourceClaimStatus{}
	if errs := resourcevalidation.ValidateResourceClaimStatusUpdate(internal, previous); len(errs) > 0 {
		t.Fatalf("API rejects allocation: %v", errs)
	}
	updatedPod := &corev1.Pod{}
	if err := sb.Get(ctx, client.ObjectKeyFromObject(pod), updatedPod); err != nil {
		t.Fatal(err)
	}
	if len(updatedPod.Status.ExtendedResourceClaimStatus.RequestMappings) != 3 {
		t.Fatal("missing socket/container mappings")
	}
	oldPod, newPod := &coreapi.Pod{}, &coreapi.Pod{}
	if err := scheme.Convert(pod, oldPod, nil); err != nil {
		t.Fatal(err)
	}
	if err := scheme.Convert(updatedPod, newPod, nil); err != nil {
		t.Fatal(err)
	}
	if errs := corevalidation.ValidatePodStatusUpdate(newPod, oldPod, corevalidation.PodValidationOptions{}); len(errs) > 0 {
		t.Fatalf("API rejects container mappings: %v", errs)
	}
	before := claim.DeepCopy()
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err != nil {
		t.Fatal(err)
	}
	if err := sb.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
		t.Fatal(err)
	}
	if !equality.Semantic.DeepEqual(before.Status.Allocation, claim.Status.Allocation) {
		t.Fatal("retry rewrote allocation/share IDs")
	}
	resources.CoreBitmap = bitmaputil.String(bitmaputil.New(0))
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err == nil {
		t.Fatal("reused claim for a changed Slurm allocation")
	}
}

func TestSocketClaimRecoveryAfterAmbiguousWrites(t *testing.T) {
	for _, failure := range []string{"create", "allocate", "pod"} {
		t.Run(failure, func(t *testing.T) {
			injected := false
			inject := func(stage string) error {
				if stage == failure && !injected {
					injected = true
					return errors.New("response lost after persistence")
				}
				return nil
			}
			sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if err := c.Create(ctx, obj, opts...); err != nil {
						return err
					}
					return inject("create")
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if err := c.SubResource(name).Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
					switch obj.(type) {
					case *resourcev1.ResourceClaim:
						return inject("allocate")
					case *corev1.Pod:
						return inject("pod")
					}
					return nil
				},
			})
			if err := sb.manageResourceClaim(context.Background(), pod, "node", resources); err == nil {
				t.Fatal("failure was not injected")
			}
			// Retry with a new bridge instance and the persisted claim.
			restarted := &SlurmBridge{Client: sb.Client, draRegistry: testutils.DRARegistryWithExampleGPU()}
			if err := restarted.manageResourceClaim(context.Background(), pod, "node", resources); err != nil {
				t.Fatal(err)
			}
			claims := &resourcev1.ResourceClaimList{}
			if err := sb.List(context.Background(), claims); err != nil {
				t.Fatal(err)
			}
			if len(claims.Items) != 1 || claims.Items[0].Status.Allocation == nil {
				t.Fatal("retry duplicated or lost reservation")
			}
			for _, config := range claims.Items[0].Status.Allocation.Devices.Config {
				if !json.Valid(config.Opaque.Parameters.Raw) {
					t.Fatal("invalid opaque configuration")
				}
			}
		})
	}
}

func TestSocketClaimRecoveryAfterGRESRegistration(t *testing.T) {
	sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{})
	ctx := context.Background()
	registeredExtra := resources.NodeExtra
	fingerprint, err := dra.AppliedCPUFingerprint(registeredExtra)
	if err != nil {
		t.Fatal(err)
	}
	resources.NodeExtra, err = dra.WithAppliedCPUFingerprint(`slurm-bridge.dra-gres-map={"v":1,"profiles":{}}`, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	// Hybrid reconciliation can temporarily revoke GRES registration while the
	// CPU topology and Slurm allocation remain unchanged.
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err == nil || !strings.Contains(err.Error(), "does not contain device profile") {
		t.Fatalf("got %v, want pending GRES registration", err)
	}
	claim := &resourcev1.ResourceClaim{}
	key := client.ObjectKey{Namespace: pod.Namespace, Name: allocationClaimName(pod)}
	if err := sb.Get(ctx, key, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Status.Allocation != nil {
		t.Fatal("claim was allocated without registered GRES")
	}
	before := claim.DeepCopy()
	resources.NodeExtra = registeredExtra
	if err := sb.manageResourceClaim(ctx, pod, "node", resources); err != nil {
		t.Fatalf("retry after GRES registration: %v", err)
	}
	if err := sb.Get(ctx, key, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Status.Allocation == nil || !equality.Semantic.DeepEqual(before.Spec, claim.Spec) {
		t.Fatal("retry did not complete the existing claim")
	}
	claims := &resourcev1.ResourceClaimList{}
	if err := sb.List(ctx, claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("retry created %d claims, want 1", len(claims.Items))
	}
}

func TestSocketClaimsRequireRegisteredFingerprint(t *testing.T) {
	for _, change := range []string{"generation", "missing fingerprint", "different CPUs"} {
		t.Run(change, func(t *testing.T) {
			sb, pod, resources := socketClaimFixture(t, interceptor.Funcs{})
			ctx := context.Background()
			inventory := &resourcev1.ResourceSlice{}
			if err := sb.Get(ctx, client.ObjectKey{Name: "cpu-pool"}, inventory); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "generation":
				inventory.Spec.Pool.Generation++
			case "missing fingerprint":
				resources.NodeExtra = `slurm-bridge.dra-gres-map={"v":1,"profiles":{}}`
			case "different CPUs":
				for i := range inventory.Spec.Devices {
					inventory.Spec.Devices[i].Attributes[nodeinfo.DraDriverCpu_CPUIDs] = resourcev1.DeviceAttribute{StringValue: ptr.To(fmt.Sprintf("%d-%d", 128+i*64, 191+i*64))}
					inventory.Spec.Devices[i].Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("128-255>32")}
				}
			}
			if err := sb.Update(ctx, inventory); err != nil {
				t.Fatal(err)
			}
			err := sb.manageResourceClaim(ctx, pod, "node", resources)
			wantSuccess := change == "generation"
			if (err == nil) != wantSuccess {
				t.Fatalf("want success=%v, got %v", wantSuccess, err)
			}
			claims := &resourcev1.ResourceClaimList{}
			if err := sb.List(ctx, claims); err != nil {
				t.Fatal(err)
			}
			if !wantSuccess && len(claims.Items) != 0 {
				t.Fatal("unverified topology created a claim")
			}
		})
	}
}
