// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	tf "k8s.io/kubernetes/pkg/scheduler/testing/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestSlurmBridge_PreBind_DRA(t *testing.T) {
	for _, tt := range []struct {
		name        string
		slurmName   string
		missingNode bool
	}{
		{name: "external", slurmName: "kube-worker-1"},
		{name: "hybrid", slurmName: "slurm-bridge-1"},
		{name: "missing node", slurmName: "slurm-bridge-1", missingNode: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pod := &corev1.Pod{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
				ObjectMeta: metav1.ObjectMeta{
					Name: "gpu-pod", Namespace: metav1.NamespaceDefault, UID: "gpu-pod-uid",
					Labels: map[string]string{wellknown.LabelPlaceholderJobId: "1"},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "gpu",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "gpu.example.com"): resource.MustParse("1"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "gpu.example.com"): resource.MustParse("1"),
						},
					},
				}}},
			}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "kube-worker-1"}}
			if tt.slurmName != node.Name {
				node.Labels = map[string]string{wellknown.LabelSlurmNodeName: tt.slurmName}
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod)
			if !tt.missingNode {
				builder.WithObjects(node)
			}
			cs := clientsetfake.NewClientset(pod.DeepCopy(), &resourcev1.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu.example.com"},
			})
			// The fake API server does not implement GenerateName.
			cs.PrependReactor("create", "resourceclaims", func(action kubetesting.Action) (bool, runtime.Object, error) {
				claim := action.(kubetesting.CreateAction).GetObject().(*resourcev1.ResourceClaim)
				claim.Name = claim.GenerateName + "-claim"
				return false, nil, nil
			})
			handle, err := tf.NewFramework(ctx, []tf.RegisterPluginFunc{
				tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
				tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
			}, "slurm-bridge", fwkruntime.WithClientSet(cs))
			if err != nil {
				t.Fatal(err)
			}
			slurm := slurmfake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
					layout, ok := obj.(*slurmtypes.V0044NodeResourceLayout)
					if !ok || key != "1" {
						return fmt.Errorf("unexpected Slurm lookup: %s %T", key, obj)
					}
					layout.V0044NodeResourceLayoutList = []api.V0044NodeResourceLayout{{
						Node: tt.slurmName,
						Gres: &api.V0044NodeGresLayoutList{{
							Name: "gpu", Type: ptr.To("gpu.example.com"), Count: ptr.To(int64(8)), Index: ptr.To("0-7"),
						}},
					}}
					return nil
				},
			}).Build()
			sb := &SlurmBridge{
				Client: builder.Build(), handle: handle,
				slurmControl: slurmcontrol.NewControl(slurm, "kubernetes", "slurm-bridge"),
			}
			status := sb.PreBind(ctx, nil, pod, node.Name)
			if tt.missingNode {
				if status.IsSuccess() {
					t.Fatal("PreBind succeeded without the Kubernetes node")
				}
				return
			}
			if !status.IsSuccess() {
				t.Fatalf("PreBind failed: %v", status)
			}
			claims, err := cs.ResourceV1().ResourceClaims(pod.Namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(claims.Items) != 1 {
				t.Fatalf("created %d ResourceClaims, want 1", len(claims.Items))
			}
			claim := &claims.Items[0]
			if claim.Status.Allocation == nil || len(claim.Status.Allocation.Devices.Results) != 8 {
				t.Fatalf("expected eight allocated GPUs, got %+v", claim.Status.Allocation)
			}
			for i, device := range claim.Status.Allocation.Devices.Results {
				if device.Pool != node.Name || device.Device != fmt.Sprintf("gpu-%d", i) {
					t.Errorf("unexpected device allocation: %+v", device)
				}
			}
			gotPod, err := cs.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			claimStatus := gotPod.Status.ExtendedResourceClaimStatus
			if claimStatus == nil || claimStatus.ResourceClaimName != claim.Name || len(claimStatus.RequestMappings) != 1 {
				t.Fatalf("pod does not reference its GPU claim: %+v", claimStatus)
			}
		})
	}
}
