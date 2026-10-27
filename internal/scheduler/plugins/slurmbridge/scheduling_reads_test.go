// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	fwk "k8s.io/kube-scheduler/framework"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/utils/ptr"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestSchedulingDiscoveryUsesCache(t *testing.T) {
	for _, phase := range []api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING, api.V0044JobInfoJobStateRUNNING} {
		t.Run(string(phase), func(t *testing.T) {
			pod := allocationPod("pod", 0, "10", "")
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			job := &api.V0044JobInfo{JobId: ptr.To(int32(10)), Nodes: ptr.To("node1"),
				AdminComment: ptr.To(`{"pods":["test/pod/pod"]}`), JobState: &[]api.V0044JobInfoJobState{phase}}
			if phase == api.V0044JobInfoJobStatePENDING {
				job.Nodes = nil
			}
			slurmNode := slurmNode("node1", "batch")
			base := fake.NewClientBuilder().WithObjects(job, &slurmNode).Build()
			reads, updates := 0, 0
			c := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(context.Context, object.ObjectKey, object.Object, ...slurmclient.GetOption) error {
					reads++
					return errors.New("unexpected live scheduling read")
				},
				List: func(context.Context, object.ObjectList, ...slurmclient.ListOption) error {
					reads++
					return errors.New("unexpected live scheduling list")
				},
				Update: func(context.Context, object.Object, any, ...slurmclient.UpdateOption) error {
					updates++
					return nil
				},
			})
			kc := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy(), node).Build()
			handle, err := fwkruntime.NewFramework(t.Context(), nil, nil,
				fwkruntime.WithClientSet(clientsetfake.NewClientset()),
				fwkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(nil, []*corev1.Node{node})))
			if err != nil {
				t.Fatal(err)
			}
			sb := &SlurmBridge{Client: kc, slurmControl: newTestSlurmControl(t, c, "", "batch"),
				handle: handle, kubeNodeIndex: testKubeNodeIndex(t, kc)}
			state := framework.NewCycleState()
			result, status := sb.PreFilter(t.Context(), state, pod, nil)
			if !status.IsSuccess() {
				t.Fatalf("PreFilter: %v", status)
			}
			if phase == api.V0044JobInfoJobStateRUNNING && (result == nil || !result.NodeNames.Has("node1")) {
				t.Fatalf("cached allocation did not select node1: %v", result)
			}
			_, status = sb.PostFilter(t.Context(), state, pod, framework.NewNodeToStatus(map[string]*fwk.Status{
				"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
			}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)))
			if !status.IsSuccess() || reads != 0 {
				t.Fatalf("PostFilter=%v, live reads=%d", status, reads)
			}
			if (updates > 0) != (phase == api.V0044JobInfoJobStatePENDING) {
				t.Fatalf("phase=%s, eligibility updates=%d", phase, updates)
			}
		})
	}
}

func TestCachedPlacementRequiresLiveConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  slurmcontrol.ExternalJob
		err  error
		want fwk.Code
	}{
		{name: "unchanged", job: slurmcontrol.ExternalJob{JobId: 10, Nodes: "node1"}, want: fwk.Success},
		{name: "gone", want: fwk.Unschedulable},
		{name: "replaced", job: slurmcontrol.ExternalJob{JobId: 11, Nodes: "node1"}, want: fwk.Unschedulable},
		{name: "different hetjob", job: slurmcontrol.ExternalJob{JobId: 10, HetJobId: 9, Nodes: "node1"}, want: fwk.Unschedulable},
		{name: "moved", job: slurmcontrol.ExternalJob{JobId: 10, Nodes: "node2"}, want: fwk.Unschedulable},
		{name: "pending", job: slurmcontrol.ExternalJob{JobId: 10, Nodes: "node1", Pending: true}, want: fwk.Unschedulable},
		{name: "finished", job: slurmcontrol.ExternalJob{JobId: 10, Nodes: "node1", Finished: true}, want: fwk.Unschedulable},
		{name: "unavailable", err: errors.New("Slurm unavailable"), want: fwk.Error},
	} {
		for _, operation := range []string{"cancel", "missing node", "bind"} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				pod := allocationPod("pod", 0, "10", "10")
				pod.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node1"}
				node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
				control := &placementConfirmationControl{live: tc.job, err: tc.err}
				kc := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy(), node).Build()
				handle, err := fwkruntime.NewFramework(t.Context(), nil, nil,
					fwkruntime.WithClientSet(clientsetfake.NewClientset()), fwkruntime.WithPodActivator(&activateRecorder{}))
				if err != nil {
					t.Fatal(err)
				}
				sb := &SlurmBridge{Client: kc, slurmControl: control, handle: handle}
				ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{*pod}}}}}
				state := framework.NewCycleState()
				state.Write(stateKey, &stateData{slurmJobIR: ir, allocation: []slurmcontrol.ExternalJob{{JobId: 10, Nodes: "node1"}}})
				var status *fwk.Status
				wantStatus := tc.want
				switch operation {
				case "cancel":
					_, status = sb.PostFilter(t.Context(), state, pod, framework.NewNodeToStatus(map[string]*fwk.Status{
						"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin("NodeAffinity"),
					}, nil))
				case "missing node":
					// The cached allocation names a deleted node, but Slurm may
					// already have moved or requeued the allocation elsewhere.
					pod.Annotations = nil
					sb.Client = kubefake.NewClientBuilder().WithObjects(pod.DeepCopy()).Build()
					sb.kubeNodeIndex = testKubeNodeIndex(t, sb.Client)
					_, status = sb.PreFilter(t.Context(), state, pod, nil)
					if wantStatus == fwk.Success {
						wantStatus = fwk.Unschedulable
					}
				default:
					status = sb.PreBind(t.Context(), state, pod, "node1")
				}
				if status.Code() != wantStatus || control.reads != 1 {
					t.Fatalf("status=%v, live reads=%d; want %v, 1", status, control.reads, wantStatus)
				}
				wantEffects := 0
				if tc.want == fwk.Success {
					wantEffects = 1
				}
				if control.effects != wantEffects {
					t.Fatalf("side effects=%d, want %d", control.effects, wantEffects)
				}
			})
		}
	}
}

type placementConfirmationControl struct {
	postFilterSlurmControl
	live           slurmcontrol.ExternalJob
	err            error
	reads, effects int
}

func (c *placementConfirmationControl) GetAllocation(context.Context, *corev1.Pod, *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	return []slurmcontrol.ExternalJob{{JobId: 10, Nodes: "node1"}}, nil
}

func (c *placementConfirmationControl) GetJob(context.Context, *corev1.Pod) (*slurmcontrol.ExternalJob, error) {
	c.reads++
	return ptr.To(c.live), c.err
}

func (c *placementConfirmationControl) DeleteJob(context.Context, *corev1.Pod) error {
	c.effects++
	return nil
}

func (c *placementConfirmationControl) GetResources(context.Context, *corev1.Pod, string) (*slurmcontrol.NodeResources, error) {
	c.effects++
	return &slurmcontrol.NodeResources{}, nil
}
