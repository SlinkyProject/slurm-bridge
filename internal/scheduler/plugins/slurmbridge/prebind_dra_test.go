// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
