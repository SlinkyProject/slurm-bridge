// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

func TestLifecycleWithoutLWSLeader(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		phase                                   corev1.PodPhase
		liveJob, indexed, lateWorker, unlabeled bool
		wantDelete, wantRelease                 bool
	}{
		{name: "running worker without allocation", phase: corev1.PodRunning, wantDelete: true},
		{name: "terminal worker without allocation", phase: corev1.PodSucceeded, wantRelease: true},
		{name: "pending worker cannot register", phase: corev1.PodPending},
		{name: "running allocation retained", phase: corev1.PodRunning, liveJob: true, indexed: true},
		{name: "uncached allocation retained", phase: corev1.PodSucceeded, liveJob: true},
		{name: "terminal allocation canceled", phase: corev1.PodSucceeded, liveJob: true, indexed: true, wantRelease: true},
		{name: "late worker retains unlabeled member", phase: corev1.PodSucceeded, liveJob: true, indexed: true, lateWorker: true, unlabeled: true},
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
			worker := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns", Name: "worker", UID: "worker-uid",
				Labels:          map[string]string{wellknown.LabelExternalJobId: "100", lwsv1.GroupUniqueHashLabelKey: "replica-1", lwsv1.WorkerIndexLabelKey: "1"},
				Finalizers:      []string{wellknown.FinalizerScheduler},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: lwsv1.GroupVersion.String(), Kind: "LeaderWorkerSet", Name: lws.Name, UID: lws.UID, Controller: ptr.To(true)}},
			}, Spec: corev1.PodSpec{SchedulerName: schedulerName, NodeName: "node"}, Status: corev1.PodStatus{Phase: tc.phase}}
			if tc.unlabeled {
				delete(worker.Labels, wellknown.LabelExternalJobId)
			}
			if tc.phase == corev1.PodPending {
				worker.Spec.NodeName = ""
				worker.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}
			}
			claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns", Name: "claim", UID: "claim-uid",
				Labels:          map[string]string{wellknown.LabelAllocationPodUID: string(worker.UID)},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Pod", UID: worker.UID}},
			}}
			other := worker.DeepCopy()
			other.Name, other.UID = "other", "other-uid"
			other.Labels = map[string]string{lwsv1.GroupUniqueHashLabelKey: "replica-2", lwsv1.WorkerIndexLabelKey: "1"}
			other.Status.Phase = corev1.PodRunning
			objects := []client.Object{lws, worker, claim, other}
			if tc.lateWorker {
				late := worker.DeepCopy()
				late.Name, late.UID = "a-late", "late-uid"
				late.Labels = map[string]string{lwsv1.GroupUniqueHashLabelKey: "replica-1", lwsv1.WorkerIndexLabelKey: "2"}
				late.Finalizers = nil
				late.Status.Phase, late.Spec.NodeName = corev1.PodPending, ""
				late.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}
				objects = append(objects, late)
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			var records []object.RuntimeObject
			if tc.liveJob {
				info := externaljobinfo.ExternalJobInfo{Pods: []string{"ns/leader/leader-uid", externaljobinfo.PodKey(worker)}}
				records = append(records, &api.V0044JobInfo{JobId: ptr.To(int32(100)), AdminComment: ptr.To(info.ToString()), JobState: ptr.To([]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING})})
			}
			live := slurmfake.NewFakeClient(records...)
			cached := slurmfake.NewFakeClient()
			if tc.indexed {
				cached = slurmfake.NewFakeClient(records...)
			}
			slurm := interceptor.NewClient(live, interceptor.Funcs{Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
				if (&slurmclient.GetOptions{}).ApplyOptions(opts).SkipCache {
					return live.Get(ctx, key, obj, opts...)
				}
				return cached.Get(ctx, key, obj, opts...)
			}})
			r := NewReconciler(kube, slurm, schedulerName, nil, nil, "", "")
			r.informer = &registrationInformer{InformerCache: cached.GetInformer(api.ObjectTypeV0044JobInfo), synced: true}
			var err error
			r.jobIndex, err = slurmjob.NewPodIndex(slurm, r.informer)
			if err != nil {
				t.Fatal(err)
			}
			r.submit = registrationSubmitFunc(func(context.Context, *slurmjobir.SlurmJobIR) (int32, error) {
				t.Fatal("submitted an allocation without the LWS leader")
				return 0, nil
			})
			queue := []Request{podRequest(worker)}
			r.enqueue = func(key Request) { queue = append(queue, key) }
			for len(queue) > 0 {
				key := queue[0]
				queue = queue[1:]
				if _, err := r.Reconcile(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			}
			if err := kube.Get(t.Context(), client.ObjectKeyFromObject(worker), worker); err != nil {
				t.Fatal(err)
			}
			if (worker.DeletionTimestamp != nil) != tc.wantDelete {
				t.Fatalf("worker deletion timestamp=%v, want deletion=%t", worker.DeletionTimestamp, tc.wantDelete)
			}
			if slices.Contains(worker.Finalizers, wellknown.FinalizerScheduler) == tc.wantRelease {
				t.Fatalf("worker finalizers=%v, want release=%t", worker.Finalizers, tc.wantRelease)
			}
			if err := kube.Get(t.Context(), client.ObjectKeyFromObject(claim), claim); apierrors.IsNotFound(err) != tc.wantRelease || err != nil && !apierrors.IsNotFound(err) {
				t.Fatalf("claim lookup=%v, want release=%t", err, tc.wantRelease)
			}
			if tc.phase == corev1.PodPending && !hasRegistrationGate(worker) {
				t.Fatal("removed the registration gate without a leader")
			}
			if err := kube.Get(t.Context(), client.ObjectKeyFromObject(other), other); err != nil || other.DeletionTimestamp != nil || !slices.Contains(other.Finalizers, wellknown.FinalizerScheduler) {
				t.Fatalf("changed another LWS replica: pod=%+v err=%v", other, err)
			}
		})
	}
}
