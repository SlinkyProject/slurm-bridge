// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type getResourcesSpy struct {
	slurmcontrol.SlurmControlInterface
	gotNodeName string
	resourceErr error
}

func (s *getResourcesSpy) GetAllocation(_ context.Context, _ *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	return testAllocation(ir), nil
}

func (s *getResourcesSpy) GetJob(context.Context, *corev1.Pod) (*slurmcontrol.ExternalJob, error) {
	return &slurmcontrol.ExternalJob{JobId: 1, Nodes: "slurm-worker-0"}, nil
}

func (s *getResourcesSpy) GetResources(ctx context.Context, pod *corev1.Pod, nodeName string) (*slurmcontrol.NodeResources, error) {
	s.gotNodeName = nodeName
	if s.resourceErr != nil {
		return nil, s.resourceErr
	}
	return &slurmcontrol.NodeResources{}, nil
}

func TestSlurmBridge_PreBind_GetResources(t *testing.T) {
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      "pod",
			UID:       "test-pod",
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
		draRegistry:  dra.DefaultRegistry(),
	}

	ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{*pod}}}}}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "uses Slurm node name"},
		{name: "propagates resource lookup failure", err: errors.New("resource layout request failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy.resourceErr, spy.gotNodeName = tc.err, ""
			state := framework.NewCycleState()
			state.Write(stateKey, &stateData{slurmJobIR: ir, allocation: testAllocation(ir)})
			status := sb.PreBind(ctx, state, pod, node.Name)
			if tc.err != nil {
				if status.Code() != fwk.Error || !errors.Is(status.AsError(), tc.err) {
					t.Fatalf("PreBind() status = %v, want resource lookup error %v", status, tc.err)
				}
			} else if !status.IsSuccess() {
				t.Fatalf("PreBind() status = %v, want success", status)
			}
			if spy.gotNodeName != "slurm-worker-0" {
				t.Fatalf("GetResources nodeName = %q, want slurm-worker-0", spy.gotNodeName)
			}
		})
	}
}
