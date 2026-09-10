// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"testing"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurminterceptor "github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type getResourcesSpy struct {
	slurmcontrol.SlurmControlInterface
	gotNodeName string
}

func (s *getResourcesSpy) GetResources(ctx context.Context, pod *corev1.Pod, nodeName string) (*slurmcontrol.NodeResources, error) {
	s.gotNodeName = nodeName
	return &slurmcontrol.NodeResources{}, nil
}

func TestSlurmBridge_PreBind_GetResourcesUsesSlurmNodeName(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      "pod",
			Labels:    map[string]string{wellknown.LabelExternalJobId: "1"},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kube-worker-1",
			Labels: map[string]string{
				wellknown.LabelSlurmNodeName: "slurm-worker-0",
			},
		},
	}
	spy := &getResourcesSpy{}
	sb := &SlurmBridge{
		Client:       fake.NewClientBuilder().WithObjects(node).Build(),
		slurmControl: spy,
	}

	if st := sb.PreBind(ctx, nil, pod, node.Name); st != nil && st.Code() != fwk.Success {
		t.Fatalf("PreBind() status = %v, want success", st)
	}
	if spy.gotNodeName != "slurm-worker-0" {
		t.Fatalf("GetResources nodeName = %q, want slurm-worker-0", spy.gotNodeName)
	}
}

func TestSlurmBridge_PreBind_MixedGPUNodeAllocation(t *testing.T) {
	ctx := context.Background()
	// Slurm includes every GRES type in the job on each node, even when that
	// type has no allocation (and therefore no index) on the selected node.
	layout := &slurmtypes.V0044NodeResourceLayout{
		V0044NodeResourceLayoutList: api.V0044NodeResourceLayoutList{
			{
				Node: "example-worker",
				Gres: &api.V0044NodeGresLayoutList{
					{Name: "gpu", Type: ptr.To(nodeinfo.DraExampleDriver), Count: ptr.To[int64](4), Index: ptr.To("0-3")},
					{Name: "gpu", Type: ptr.To(nodeinfo.DraDriverGpuNvidia), Count: ptr.To[int64](0)},
				},
			},
			{
				Node: "nvidia-worker",
				Gres: &api.V0044NodeGresLayoutList{
					{Name: "gpu", Type: ptr.To(nodeinfo.DraExampleDriver), Count: ptr.To[int64](0)},
					{Name: "gpu", Type: ptr.To(nodeinfo.DraDriverGpuNvidia), Count: ptr.To[int64](8), Index: ptr.To("0-7")},
				},
			},
		},
	}
	slurm := slurmfake.NewClientBuilder().WithInterceptorFuncs(slurminterceptor.Funcs{
		Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
			*obj.(*slurmtypes.V0044NodeResourceLayout) = *layout.DeepCopy()
			return nil
		},
	}).Build()
	kubeclient := fake.NewClientBuilder().WithObjects(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "example-worker"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "nvidia-worker"}},
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraExampleDriver}},
		&resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: nodeinfo.DraDriverGpuNvidia}},
	).Build()
	sb := &SlurmBridge{
		Client:       kubeclient,
		slurmControl: slurmcontrol.NewControl(slurm, "kubernetes", "slurm-bridge"),
	}
	for _, node := range layout.V0044NodeResourceLayoutList {
		t.Run(node.Node, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: metav1.NamespaceDefault,
					Name:      node.Node + "-pod",
					Labels:    map[string]string{wellknown.LabelExternalJobId: "11"},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker"}}},
			}
			if st := sb.PreBind(ctx, nil, pod, node.Node); st != nil && st.Code() != fwk.Success {
				t.Fatalf("PreBind() status = %v, want success", st)
			}
		})
	}
	claims := &resourcev1.ResourceClaimList{}
	if err := kubeclient.List(ctx, claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 0 {
		t.Fatalf("created %d claims for pods without device requests, want 0", len(claims.Items))
	}
}
