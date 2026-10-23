// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type unregisteredControl struct {
	slurmcontrol.SlurmControlInterface
}

func (*unregisteredControl) GetJob(context.Context, *corev1.Pod) (*slurmcontrol.ExternalJob, error) {
	return &slurmcontrol.ExternalJob{}, nil
}

func (*unregisteredControl) GetAllocation(context.Context, *corev1.Pod, *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	return nil, slurmjob.ErrAllocationMissing
}

func TestPostFilterWaitsForRegistration(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod"}}
	sb := &SlurmBridge{slurmControl: &unregisteredControl{}}
	state := framework.NewCycleState()
	state.Write(stateKey, &stateData{slurmJobIR: &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{*pod}}}}}})
	_, status := sb.PostFilter(t.Context(), state, pod, nil)
	if status.Code() != fwk.Unschedulable {
		t.Fatalf("PostFilter = %v; want wait for controller registration", status)
	}
}

type releaseControl struct {
	slurmcontrol.SlurmControlInterface
	calls []string
	fail  bool
}

func (*releaseControl) GetAllocation(_ context.Context, _ *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	return testAllocation(ir), nil
}

func (c *releaseControl) UpdateJob(_ context.Context, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) (int32, error) {
	c.calls = append(c.calls, "update "+pod.Name)
	if c.fail && pod.Name == "second" {
		return 0, errors.New("eligibility failed")
	}
	if ir.Components[ir.ComponentOf(pod.Namespace, pod.Name)].JobInfo.Priority != nil {
		return 0, errors.New("priority can release the hold")
	}
	return 1, nil
}
func (c *releaseControl) ReleaseJob(_ context.Context, id int32, _ *int32) error {
	c.calls = append(c.calls, fmt.Sprintf("release %d", id))
	return nil
}

func TestHetjobEligibilityPrecedesRelease(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			control := &releaseControl{fail: fail}
			sb := &SlurmBridge{slurmControl: control}
			ir := &slurmjobir.SlurmJobIR{}
			for i, name := range []string{"first", "second"} {
				ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{Pods: corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{wellknown.LabelExternalJobId: fmt.Sprint(100 + 7*i)}}}}}, JobInfo: slurmjobir.SlurmJobIRJobInfo{Priority: ptr.To(int32(100))}})
			}
			status := sb.updateExternalJob(t.Context(), &stateData{slurmJobIR: ir}, true)
			if status.IsSuccess() == fail {
				t.Fatalf("status=%v", status)
			}
			want := []string{"update first", "update second"}
			if !fail {
				want = append(want, "release 100", "release 107")
			}
			if !reflect.DeepEqual(control.calls, want) {
				t.Fatalf("calls=%v; want %v", control.calls, want)
			}
		})
	}
}

func TestPendingJobDoesNotRepeatRelease(t *testing.T) {
	control := &releaseControl{}
	sb := &SlurmBridge{slurmControl: control}
	ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "first"}}}}}}}
	if status := sb.updateExternalJob(t.Context(), &stateData{slurmJobIR: ir}, false); !status.IsSuccess() {
		t.Fatal(status)
	}
	if !reflect.DeepEqual(control.calls, []string{"update first"}) {
		t.Fatalf("released an already queued job: %v", control.calls)
	}
}
