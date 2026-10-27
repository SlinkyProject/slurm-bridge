// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestLifecycleLostLWSMember(t *testing.T) {
	for _, tc := range []struct {
		name string
		// lws marks the group as a LeaderWorkerSet replica.
		lws         bool
		workerPhase corev1.PodPhase
		// unbound and deleting describe a worker lost before being bound.
		unbound, deleting bool
		wantCancel        bool
	}{
		{name: "failed worker cancels the group allocation", lws: true, workerPhase: corev1.PodFailed, wantCancel: true},
		{name: "deleted unbound worker cancels the group allocation", lws: true, workerPhase: corev1.PodPending, unbound: true, deleting: true, wantCancel: true},
		{name: "running group keeps its allocation", lws: true, workerPhase: corev1.PodRunning},
		{name: "bound worker still terminating keeps the allocation", lws: true, workerPhase: corev1.PodRunning, deleting: true},
		{name: "non-LWS group waits for every member", workerPhase: corev1.PodFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, resourcev1.AddToScheme, lwsv1.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}
			lws := &lwsv1.LeaderWorkerSet{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "lws", UID: "lws-uid"}}
			lws.Spec.LeaderWorkerTemplate.Size = ptr.To(int32(2))
			newMember := func(name, index string) *corev1.Pod {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Namespace: "ns", Name: name, UID: types.UID("uid-" + name),
					Labels:     map[string]string{wellknown.LabelExternalJobId: "100"},
					Finalizers: []string{wellknown.FinalizerScheduler},
				}, Spec: corev1.PodSpec{SchedulerName: schedulerName, NodeName: "node-" + index}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
				if tc.lws {
					pod.Labels[lwsv1.SetNameLabelKey] = lws.Name
					pod.Labels[lwsv1.GroupUniqueHashLabelKey] = "replica-0"
					pod.Labels[lwsv1.WorkerIndexLabelKey] = index
					pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: lwsv1.GroupVersion.String(), Kind: "LeaderWorkerSet", Name: lws.Name, UID: lws.UID, Controller: ptr.To(true)}}
				}
				return pod
			}
			leader := newMember("leader", "0")
			worker := newMember("worker", "1")
			worker.Status.Phase = tc.workerPhase
			if tc.unbound {
				worker.Spec.NodeName = ""
			}
			if tc.deleting {
				worker.DeletionTimestamp = ptr.To(metav1.Now())
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(lws, leader, worker).Build()

			info := externaljobinfo.ExternalJobInfo{Pods: []string{externaljobinfo.PodKey(leader), externaljobinfo.PodKey(worker)}}
			record := &api.V0044JobInfo{JobId: ptr.To(int32(100)), AdminComment: ptr.To(info.ToString()), JobState: ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING})}
			live := slurmfake.NewFakeClient(record)
			cached := slurmfake.NewFakeClient(record.DeepCopy())
			var canceled []int32
			slurm := interceptor.NewClient(live, interceptor.Funcs{
				Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
					if (&slurmclient.GetOptions{}).ApplyOptions(opts).SkipCache {
						return live.Get(ctx, key, obj, opts...)
					}
					return cached.Get(ctx, key, obj, opts...)
				},
				Delete: func(ctx context.Context, obj object.Object, opts ...slurmclient.DeleteOption) error {
					canceled = append(canceled, ptr.Deref(obj.(*api.V0044JobInfo).JobId, 0))
					// Stand in for the informer observing the cancellation.
					if err := cached.Delete(ctx, obj.DeepCopyObject().(object.Object), opts...); err != nil {
						return err
					}
					return live.Delete(ctx, obj, opts...)
				},
			})
			r := NewReconciler(kube, slurm, schedulerName, nil, nil, "", "")
			r.informer = &registrationInformer{InformerCache: cached.GetInformer(api.ObjectTypeV0044JobInfo), synced: true}
			var err error
			r.jobIndex, err = slurmjob.NewPodIndex(slurm, r.informer)
			if err != nil {
				t.Fatal(err)
			}
			r.submit = registrationSubmitFunc(func(context.Context, *slurmjobir.SlurmJobIR) (int32, error) {
				t.Fatal("submitted an allocation while releasing the group")
				return 0, nil
			})
			queue := []Request{podRequest(worker)}
			r.enqueue = func(key Request) { queue = append(queue, key) }
			for i := 0; len(queue) > 0 && i < 20; i++ {
				key := queue[0]
				queue = queue[1:]
				if _, err := r.Reconcile(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			}

			if got := slices.Contains(canceled, 100); got != tc.wantCancel {
				t.Fatalf("canceled jobs=%v, want job 100 canceled=%t", canceled, tc.wantCancel)
			}
			if !tc.wantCancel {
				return
			}
			// The surviving leader no longer has an allocation.
			if err := kube.Get(t.Context(), client.ObjectKeyFromObject(leader), leader); err != nil {
				t.Fatal(err)
			}
			if leader.DeletionTimestamp == nil {
				t.Fatalf("surviving leader was not deleted after its allocation was canceled")
			}
		})
	}
}
