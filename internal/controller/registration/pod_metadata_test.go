// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	kubeinterceptor "sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/registration/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestMetadataRepairVerifiesOnlyChanges(t *testing.T) {
	job := func(id int32, nodes, owner string) *api.V0044JobInfo {
		return &api.V0044JobInfo{
			JobId: ptr.To(id), Nodes: ptr.To(nodes),
			AdminComment: ptr.To(`{"pods":["` + owner + `"]}`),
			JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
		}
	}
	readErr := errors.New("live Slurm read failed")
	for _, tt := range []struct {
		name      string
		cached    *api.V0044JobInfo
		live      []object.Object
		label     string
		readError bool
		wantReads int
		wantNode  string
	}{
		{name: "matching metadata needs no live read", cached: job(1, "node1", "ns/pod/current-uid"), readError: true, wantNode: "node1"},
		{name: "cache miss preserves metadata without a live read", live: []object.Object{job(1, "node1", "ns/pod/current-uid")}, wantNode: "node1"},
		{name: "stale pending state cannot erase a live node", cached: job(1, "", "ns/pod/current-uid"), live: []object.Object{job(1, "node1", "ns/pod/current-uid")}, wantReads: 1, wantNode: "node1"},
		{name: "deleted candidate cannot repair metadata", cached: job(1, "", "ns/pod/current-uid"), wantReads: 1, wantNode: "node1"},
		{name: "changed ownership cannot repair metadata", cached: job(1, "", "ns/pod/current-uid"), live: []object.Object{job(1, "", "ns/other")}, wantReads: 1, wantNode: "node1"},
		{name: "live labeled allocation is protected", cached: job(1, "node1", "ns/pod/current-uid"), label: "2", live: []object.Object{job(1, "node1", "ns/pod/current-uid"), job(2, "node1", "ns/pod/current-uid")}, wantReads: 1, wantNode: "node1"},
		{name: "requeued labeled allocation is protected", cached: job(1, "node1", "ns/pod/current-uid"), label: "2", live: []object.Object{job(1, "node1", "ns/pod/current-uid"), func() object.Object {
			requeued := job(2, "", "ns/pod/current-uid")
			requeued.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING, api.V0044JobInfoJobStatePREEMPTED}
			return requeued
		}()}, wantReads: 1, wantNode: "node1"},
		{name: "verified node mismatch is repaired", cached: job(1, "", "ns/pod/current-uid"), live: []object.Object{job(1, "node2", "ns/pod/current-uid")}, wantReads: 1},
		{name: "verification error preserves metadata", cached: job(1, "", "ns/pod/current-uid"), readError: true, wantReads: 1, wantNode: "node1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			label := tt.label
			if label == "" {
				label = "1"
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns", Name: "pod", UID: "current-uid",
				Labels:      map[string]string{wellknown.LabelExternalJobId: label},
				Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node1"},
				Finalizers:  []string{wellknown.FinalizerScheduler},
			}, Spec: corev1.PodSpec{SchedulerName: schedulerName}}
			want := pod.DeepCopy()
			want.Annotations[wellknown.AnnotationExternalJobNode] = tt.wantNode
			kube := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy()).Build()
			cache := fake.NewClientBuilder()
			if tt.cached != nil {
				cache.WithObjects(tt.cached)
			}
			base := fake.NewClientBuilder().WithObjects(tt.live...).Build()
			reads := 0
			live := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
					reads++
					if !(new(slurmclient.GetOptions).ApplyOptions(opts)).SkipCache {
						t.Error("metadata verification did not bypass the cache")
					}
					if tt.readError {
						return readErr
					}
					return base.Get(ctx, key, obj, opts...)
				},
			})
			control, err := slurmjob.NewPodIndex(live, cache.Build().GetInformer(api.ObjectTypeV0044JobInfo))
			if err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{Client: kube, jobIndex: control, schedulerName: schedulerName}
			_, err = r.reconcilePodMetadata(t.Context(), reconcile.Request{NamespacedName: kubeclient.ObjectKeyFromObject(pod)})
			if wantError := tt.readError && tt.wantReads > 0; (err != nil) != wantError {
				t.Fatalf("metadata validation error = %v, want error %t", err, wantError)
			}
			if reads != tt.wantReads {
				t.Errorf("live reads = %d, want %d", reads, tt.wantReads)
			}
			stored := &corev1.Pod{}
			if err := kube.Get(t.Context(), kubeclient.ObjectKeyFromObject(pod), stored); err != nil {
				t.Fatal(err)
			}
			for _, got := range []*corev1.Pod{stored} {
				if !reflect.DeepEqual(got.Labels, want.Labels) || !reflect.DeepEqual(got.Annotations, want.Annotations) || !reflect.DeepEqual(got.Finalizers, want.Finalizers) {
					t.Errorf("metadata = labels %v, annotations %v, finalizers %v; want %v, %v, %v", got.Labels, got.Annotations, got.Finalizers, want.Labels, want.Annotations, want.Finalizers)
				}
			}
		})
	}
}

func metadataJob(id, leader, offset int32) *api.V0044JobInfo {
	job := &api.V0044JobInfo{
		JobId: ptr.To(id), Nodes: ptr.To("node1"),
		AdminComment: ptr.To(`{"pods":["ns/pod/current-uid"]}`),
		JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
	}
	if leader > 0 {
		job.HetJobId = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(leader)}
		job.HetJobOffset = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(offset)}
	}
	return job
}

func TestMetadataRepairIdentityAndLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name                                          string
		label                                         string
		leader, offset                                int32
		phase                                         corev1.PodPhase
		deleting, bound, foreign, finished, malformed bool
		wantRepair                                    bool
	}{
		{name: "repairs stale component ID", label: "99", wantRepair: true},
		{name: "repairs zero component ID", label: "0", wantRepair: true},
		{name: "repairs hetjob leader", label: "99", leader: 2, wantRepair: true},
		{name: "repairs hetjob component", label: "99", leader: 1, offset: 1, wantRepair: true},
		{name: "removes stale hetjob labels", label: "2", wantRepair: true},
		{name: "does not adopt unlabeled Pod"},
		{name: "leaves bound node annotation alone", label: "99", bound: true, wantRepair: true},
		{name: "does not restore succeeded Pod finalizer", label: "99", phase: corev1.PodSucceeded},
		{name: "does not restore failed Pod finalizer", label: "99", phase: corev1.PodFailed},
		{name: "does not change deleting Pod identity", label: "99", deleting: true},
		{name: "ignores other schedulers", label: "99", foreign: true},
		{name: "finished job cannot restore identity or finalizer", label: "99", finished: true},
		{name: "malformed hetjob cannot repair identity", label: "99", malformed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns", Name: "pod", UID: "current-uid", Labels: map[string]string{"unrelated": "preserve"},
				Annotations: map[string]string{"unrelated": "preserve"}, Finalizers: []string{"unrelated"},
			}, Spec: corev1.PodSpec{SchedulerName: schedulerName}, Status: corev1.PodStatus{Phase: tt.phase}}
			if tt.label != "" {
				pod.Labels[wellknown.LabelExternalJobId] = tt.label
				pod.Labels[wellknown.LabelExternalHetJobId] = "98"
				pod.Labels[wellknown.LabelExternalHetJobOffset] = "9"
			}
			if tt.deleting {
				pod.DeletionTimestamp = ptr.To(metav1.Now())
			}
			if tt.bound {
				pod.Spec.NodeName = "bound-node"
				pod.Annotations[wellknown.AnnotationExternalJobNode] = "bound-node"
			}
			if tt.foreign {
				pod.Spec.SchedulerName = "another-scheduler"
			}
			job := metadataJob(2, tt.leader, tt.offset)
			if tt.finished {
				job.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCOMPLETED}
			}
			if tt.malformed {
				job.HetJobId = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(1))}
			}
			base := fake.NewClientBuilder().WithObjects(job).Build()
			reads := 0
			live := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
				reads++
				if !(new(slurmclient.GetOptions).ApplyOptions(opts)).SkipCache {
					t.Error("repair did not bypass cache")
				}
				return base.Get(ctx, key, obj, opts...)
			}})
			index, err := slurmjob.NewPodIndex(live, base.GetInformer(api.ObjectTypeV0044JobInfo))
			if err != nil {
				t.Fatal(err)
			}
			patches := 0
			kube := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy()).WithInterceptorFuncs(kubeinterceptor.Funcs{
				Patch: func(ctx context.Context, c kubeclient.WithWatch, obj kubeclient.Object, patch kubeclient.Patch, opts ...kubeclient.PatchOption) error {
					patches++
					return c.Patch(ctx, obj, patch, opts...)
				},
			}).Build()
			r := &Reconciler{Client: kube, jobIndex: index, schedulerName: schedulerName}
			req := reconcile.Request{NamespacedName: kubeclient.ObjectKeyFromObject(pod)}
			changed, err := r.reconcilePodMetadata(t.Context(), req)
			if err != nil || changed != tt.wantRepair {
				t.Fatalf("repair = %t, %v; want %t", changed, err, tt.wantRepair)
			}
			stored := &corev1.Pod{}
			if err := kube.Get(t.Context(), req.NamespacedName, stored); err != nil {
				t.Fatal(err)
			}
			want := pod.DeepCopy()
			if tt.wantRepair {
				want.Labels[wellknown.LabelExternalJobId] = "2"
				delete(want.Labels, wellknown.LabelExternalHetJobId)
				delete(want.Labels, wellknown.LabelExternalHetJobOffset)
				if tt.leader > 0 {
					want.Labels[wellknown.LabelExternalHetJobId] = strconv.Itoa(int(tt.leader))
					want.Labels[wellknown.LabelExternalHetJobOffset] = strconv.Itoa(int(tt.offset))
				}
				want.Finalizers = append(want.Finalizers, wellknown.FinalizerScheduler)
			}
			if !reflect.DeepEqual(stored.Labels, want.Labels) || !reflect.DeepEqual(stored.Annotations, want.Annotations) || !reflect.DeepEqual(stored.Finalizers, want.Finalizers) {
				t.Fatalf("metadata = %v, %v, %v; want %v, %v, %v", stored.Labels, stored.Annotations, stored.Finalizers, want.Labels, want.Annotations, want.Finalizers)
			}
			if !tt.wantRepair && (patches != 0 || reads != 0) {
				t.Fatalf("unchanged Pod made %d patches and %d live reads", patches, reads)
			}
			beforeReads, beforePatches := reads, patches
			changed, err = r.reconcilePodMetadata(t.Context(), req)
			if err != nil || changed || reads != beforeReads || patches != beforePatches {
				t.Fatalf("repeat repair = %t, %v; extra reads %d, patches %d", changed, err, reads-beforeReads, patches-beforePatches)
			}
		})
	}
}

func TestMetadataRepairConflictsWithConcurrentPodChanges(t *testing.T) {
	for _, change := range []string{"allocation", "binding", "completion"} {
		t.Run(change, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns", Name: "pod", UID: "current-uid", Labels: map[string]string{wellknown.LabelExternalJobId: "2"},
				Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "obsolete-node"},
			}, Spec: corev1.PodSpec{SchedulerName: schedulerName}}
			kube := kubefake.NewClientBuilder().WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
			job := metadataJob(2, 0, 0)
			base := fake.NewClientBuilder().WithObjects(job).Build()
			var concurrent *corev1.Pod
			live := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
				concurrent = &corev1.Pod{}
				if err := kube.Get(ctx, kubeclient.ObjectKeyFromObject(pod), concurrent); err != nil {
					return err
				}
				switch change {
				case "allocation":
					concurrent.Labels[wellknown.LabelExternalJobId] = "3"
					concurrent.Annotations[wellknown.AnnotationExternalJobNode] = "new-node"
				case "binding":
					concurrent.Spec.NodeName = "bound-node"
				case "completion":
					concurrent.Status.Phase = corev1.PodSucceeded
					if err := kube.Status().Update(ctx, concurrent); err != nil {
						return err
					}
				}
				if change != "completion" {
					if err := kube.Update(ctx, concurrent); err != nil {
						return err
					}
				}
				return base.Get(ctx, key, obj, opts...)
			}})
			index, err := slurmjob.NewPodIndex(live, base.GetInformer(api.ObjectTypeV0044JobInfo))
			if err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{Client: kube, jobIndex: index, schedulerName: schedulerName}
			req := reconcile.Request{NamespacedName: kubeclient.ObjectKeyFromObject(pod)}
			changed, err := r.reconcilePodMetadata(t.Context(), req)
			if changed || !apierrors.IsConflict(err) {
				t.Fatalf("repair = %t, %v; want conflict", changed, err)
			}
			stored := &corev1.Pod{}
			if err := kube.Get(t.Context(), req.NamespacedName, stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, concurrent) {
				t.Fatalf("concurrent Pod was overwritten: got %#v, want %#v", stored, concurrent)
			}
			if slices.Contains(stored.Finalizers, wellknown.FinalizerScheduler) {
				t.Fatal("repair restored finalizer after concurrent update")
			}
		})
	}
}

type cleanupRecorder struct {
	slurmcontrol.SlurmControlInterface
	calls        int
	beforeReturn func(context.Context) error
}

func (c *cleanupRecorder) IsJobRunning(ctx context.Context, _ *corev1.Pod) (bool, error) {
	c.calls++
	if c.beforeReturn != nil {
		return false, c.beforeReturn(ctx)
	}
	return false, nil
}

func TestSyncWaitsForMetadataPatchBeforeCleanup(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "ns", Name: "pod", UID: "current-uid", Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
	}, Spec: corev1.PodSpec{SchedulerName: schedulerName, NodeName: "node1"}}
	base := kubefake.NewClientBuilder().WithObjects(pod).Build()
	if err := base.Get(t.Context(), kubeclient.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal(err)
	}
	// Simulate a controller cache which has not observed the metadata patch.
	kube := kubeinterceptor.NewClient(base, kubeinterceptor.Funcs{Get: func(_ context.Context, _ kubeclient.WithWatch, _ kubeclient.ObjectKey, obj kubeclient.Object, _ ...kubeclient.GetOption) error {
		*obj.(*corev1.Pod) = *pod.DeepCopy()
		return nil
	}})
	slurm := fake.NewClientBuilder().WithObjects(metadataJob(2, 0, 0)).Build()
	index, err := slurmjob.NewPodIndex(slurm, slurm.GetInformer(api.ObjectTypeV0044JobInfo))
	if err != nil {
		t.Fatal(err)
	}
	cleanup := &cleanupRecorder{}
	r := &Reconciler{Client: kube, jobIndex: index, slurmControl: cleanup, schedulerName: schedulerName}
	if _, err := r.syncPod(t.Context(), reconcile.Request{NamespacedName: kubeclient.ObjectKeyFromObject(pod)}); err != nil {
		t.Fatal(err)
	}
	if cleanup.calls != 0 {
		t.Fatalf("cleanup used stale metadata %d times", cleanup.calls)
	}
	stored := &corev1.Pod{}
	if err := base.Get(t.Context(), kubeclient.ObjectKeyFromObject(pod), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Labels[wellknown.LabelExternalJobId] != "2" {
		t.Fatalf("job label = %q, want 2", stored.Labels[wellknown.LabelExternalJobId])
	}
}

func TestPodCleanupPreservesConcurrentAllocation(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "ns", Name: "pod", UID: "original", Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
	}, Spec: corev1.PodSpec{SchedulerName: schedulerName, NodeName: "node1"}}
	kube := kubefake.NewClientBuilder().WithObjects(pod).Build()
	cleanup := &cleanupRecorder{beforeReturn: func(ctx context.Context) error {
		current := &corev1.Pod{}
		if err := kube.Get(ctx, kubeclient.ObjectKeyFromObject(pod), current); err != nil {
			return err
		}
		current.Labels[wellknown.LabelExternalJobId] = "2"
		return kube.Update(ctx, current)
	}}
	r := &Reconciler{Client: kube, slurmControl: cleanup, schedulerName: schedulerName}
	err := r.syncKubernetes(t.Context(), reconcile.Request{NamespacedName: kubeclient.ObjectKeyFromObject(pod)})
	if !apierrors.IsConflict(err) {
		t.Fatalf("cleanup error = %v, want conflict", err)
	}
	current := &corev1.Pod{}
	if err := kube.Get(t.Context(), kubeclient.ObjectKeyFromObject(pod), current); err != nil {
		t.Fatal(err)
	}
	if current.Labels[wellknown.LabelExternalJobId] != "2" {
		t.Fatalf("new allocation was lost: %v", current.Labels)
	}
}
