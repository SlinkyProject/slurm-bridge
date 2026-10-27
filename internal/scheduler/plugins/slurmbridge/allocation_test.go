// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
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

func schedulerAllocation() (*slurmjobir.SlurmJobIR, []api.V0044JobInfo) {
	ir := &slurmjobir.SlurmJobIR{RootPOM: metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha3", Kind: "CompositePodGroup"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "root", UID: "root-uid"},
	}}
	var jobs []api.V0044JobInfo
	for i, id := range []int32{100, 107} {
		name := fmt.Sprintf("pod-%d", i)
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: types.UID(name),
			Labels:      map[string]string{wellknown.LabelExternalJobId: strconv.Itoa(int(id))},
			Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node"}}}
		component := slurmjobir.SlurmJobComponent{Pods: corev1.PodList{Items: []corev1.Pod{pod}},
			ObjectMeta: metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1beta1", Kind: "PodGroup"}, ObjectMeta: pod.ObjectMeta},
			JobInfo:    slurmjobir.SlurmJobIRJobInfo{Priority: ptr.To(int32(50))}}
		ir.Components = append(ir.Components, component)
		info := slurmcontrol.ExternalJobInfoFor(ir.RootPOM, component)
		jobs = append(jobs, api.V0044JobInfo{JobId: ptr.To(id), AdminComment: ptr.To(info.ToString()),
			JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
			Priority:     &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(0))},
			HetJobId:     &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(100))},
			HetJobOffset: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(i))}})
	}
	return ir, jobs
}

func TestSchedulerReleaseOrdering(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		noPriority bool
		want       []string
	}{
		{name: "all eligibility updates precede release and priority", want: []string{"update/100", "update/107", "release/100", "priority/100", "release/107", "priority/107"}},
		{name: "failed eligibility prevents release", fail: "update/107", want: []string{"update/100", "update/107"}},
		{name: "failed release prevents priority", fail: "release/100", want: []string{"update/100", "update/107", "release/100"}},
		{name: "priority failure is propagated", fail: "priority/100", want: []string{"update/100", "update/107", "release/100", "priority/100"}},
		{name: "no explicit priority", noPriority: true, want: []string{"update/100", "update/107", "release/100", "release/107"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ir, jobs := schedulerAllocation()
			if tc.noPriority {
				for i := range ir.Components {
					ir.Components[i].JobInfo.Priority = nil
				}
			}
			failure := errors.New("Slurm update failed")
			var calls []string
			c := fake.NewClientBuilder().WithObjects(&jobs[0], &jobs[1]).WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, obj object.Object, req any, _ ...slurmclient.UpdateOption) error {
					desc := req.(api.V0044JobDescMsg)
					operation := "update"
					switch {
					case desc.Hold != nil:
						operation = "release"
						if *desc.Hold || desc.Priority != nil {
							t.Fatalf("release also holds or sets priority: %+v", desc)
						}
					case desc.Priority != nil:
						operation = "priority"
						if desc.AdminComment != nil || !ptr.Deref(desc.Priority.Set, false) || ptr.Deref(desc.Priority.Number, 0) != 50 {
							t.Fatalf("priority leaked into eligibility update: %+v", desc)
						}
					}
					call := operation + "/" + string(obj.GetKey())
					calls = append(calls, call)
					if call == tc.fail {
						return failure
					}
					return nil
				},
			}).Build()
			sb := &SlurmBridge{slurmControl: newTestSlurmControl(t, c, "", "")}
			status := sb.updateExternalJob(t.Context(), &stateData{slurmJobIR: ir}, true)
			if !reflect.DeepEqual(calls, tc.want) || errors.Is(status.AsError(), failure) != (tc.fail != "") {
				t.Fatalf("calls=%v, status=%v; want %v, failure=%q", calls, status, tc.want, tc.fail)
			}
			for _, component := range ir.Components {
				if !tc.noPriority && ptr.Deref(component.JobInfo.Priority, 0) != 50 {
					t.Fatal("priority was not restored after eligibility update")
				}
			}
		})
	}
}

func TestSchedulerAllocationBeforeBind(t *testing.T) {
	for _, tc := range []struct {
		name, change, reason string
		want                 fwk.Code
	}{
		{name: "unchanged", want: fwk.Success},
		{name: "allocation replaced with fresh labels", change: "replace", reason: "changed during scheduling", want: fwk.Unschedulable},
		{name: "duplicate appears before bind", change: "duplicate", reason: "multiple active", want: fwk.Unschedulable},
		{name: "allocation disappeared", change: "missing", reason: "waiting for Slurm registration", want: fwk.Unschedulable},
		{name: "finished sibling needs no label", change: "sibling", want: fwk.Success},
		{name: "own component finished", change: "own", reason: "component has finished", want: fwk.Unschedulable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ir, jobs := schedulerAllocation()
			for i := range jobs {
				jobs[i].JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING}
				jobs[i].Nodes = ptr.To("node")
			}
			base := fake.NewClientBuilder().WithObjects(&jobs[0], &jobs[1]).Build()
			reads := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
				if _, ok := obj.(*api.V0044NodeResourceLayoutListObject); ok {
					reads++
					return nil
				}
				return base.Get(ctx, key, obj, opts...)
			}})
			sb := &SlurmBridge{Client: kubefake.NewClientBuilder().WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}).Build(), slurmControl: newTestSlurmControl(t, c, "", "")}
			s := &stateData{slurmJobIR: ir}
			pod := &ir.Components[1].Pods.Items[0]
			if status := sb.checkAllocation(t.Context(), s, pod); !status.IsSuccess() {
				t.Fatal(status)
			}
			pinned := slices.Clone(s.allocation)
			switch tc.change {
			case "replace", "missing", "duplicate":
				for i := range jobs {
					if tc.change != "duplicate" {
						if err := base.Delete(t.Context(), &jobs[i]); err != nil {
							t.Fatal(err)
						}
					}
					if tc.change != "missing" {
						jobs[i].JobId = ptr.To(*jobs[i].JobId + 100)
						jobs[i].HetJobId.Number = ptr.To(int32(200))
						if tc.change == "replace" {
							ir.Components[i].Pods.Items[0].Labels[wellknown.LabelExternalJobId] = strconv.Itoa(int(*jobs[i].JobId))
						}
						if err := base.Create(t.Context(), &jobs[i], nil); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "sibling", "own":
				finished := 0
				if tc.change == "own" {
					finished = 1
				}
				jobs[finished].JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCOMPLETED}
				if err := base.Update(t.Context(), &jobs[finished], nil); err != nil {
					t.Fatal(err)
				}
				if tc.change == "sibling" {
					ir.Components[0].Pods.Items[0].Labels = nil
				}
			}
			state := framework.NewCycleState()
			state.Write(stateKey, s)
			status := sb.PreBind(t.Context(), state, pod, "node")
			if status.Code() != tc.want || !strings.Contains(status.Message(), tc.reason) {
				t.Fatalf("PreBind=%v; want %v containing %q", status, tc.want, tc.reason)
			}
			wantReads := 0
			if tc.want == fwk.Success {
				wantReads = 1
			}
			if reads != wantReads || !reflect.DeepEqual(s.allocation, pinned) {
				t.Fatalf("resource reads=%d, allocation=%v; want pinned %v", reads, s.allocation, pinned)
			}
		})
	}
}

func TestSchedulerDuplicateBlocksCycle(t *testing.T) {
	for _, label := range []string{"100", "107"} {
		t.Run("label-"+label, func(t *testing.T) {
			ir, jobs := schedulerAllocation()
			pod := ir.Components[0].Pods.Items[0].DeepCopy()
			pod.Labels[wellknown.LabelExternalJobId] = label
			for i := range jobs {
				jobs[i].HetJobId, jobs[i].HetJobOffset = nil, nil
				jobs[i].AdminComment = ptr.To(`{"pods":["ns/pod-0/pod-0"]}`)
			}
			c := fake.NewClientBuilder().WithObjects(&jobs[0], &jobs[1]).WithInterceptorFuncs(interceptor.Funcs{
				Update: func(context.Context, object.Object, any, ...slurmclient.UpdateOption) error {
					t.Fatal("conflicted cycle updated Slurm")
					return nil
				},
				Delete: func(context.Context, object.Object, ...slurmclient.DeleteOption) error {
					t.Fatal("conflicted cycle canceled Slurm")
					return nil
				},
			}).Build()
			sb := &SlurmBridge{Client: kubefake.NewClientBuilder().WithObjects(pod).Build(), slurmControl: newTestSlurmControl(t, c, "", "")}
			state := framework.NewCycleState()
			if _, status := sb.PreFilter(t.Context(), state, pod, nil); status.Code() != fwk.Unschedulable || !strings.Contains(status.Message(), "multiple active") {
				t.Fatalf("annotated Pod bypassed conflict: %v", status)
			}
			if _, status := sb.PostFilter(t.Context(), state, pod, nil); status.Code() != fwk.Unschedulable {
				t.Fatalf("rejected cycle continued: %v", status)
			}
		})
	}
}
