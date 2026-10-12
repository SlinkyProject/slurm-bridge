// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
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
		{name: "matching metadata needs no live read", cached: job(1, "node1", "ns/pod"), readError: true, wantNode: "node1"},
		{name: "cache miss preserves metadata without a live read", live: []object.Object{job(1, "node1", "ns/pod")}, wantNode: "node1"},
		{name: "stale pending state cannot erase a live node", cached: job(1, "", "ns/pod"), live: []object.Object{job(1, "node1", "ns/pod")}, wantReads: 1, wantNode: "node1"},
		{name: "deleted candidate cannot repair metadata", cached: job(1, "", "ns/pod"), wantReads: 1, wantNode: "node1"},
		{name: "changed ownership cannot repair metadata", cached: job(1, "", "ns/pod"), live: []object.Object{job(1, "", "ns/other")}, wantReads: 1, wantNode: "node1"},
		{name: "live labeled allocation is protected", cached: job(1, "node1", "ns/pod"), label: "2", live: []object.Object{job(1, "node1", "ns/pod"), job(2, "node1", "ns/pod")}, wantReads: 1, wantNode: "node1"},
		{name: "verified node mismatch is repaired", cached: job(1, "", "ns/pod"), live: []object.Object{job(1, "node2", "ns/pod")}, wantReads: 1},
		{name: "verification error preserves metadata", cached: job(1, "", "ns/pod"), readError: true, wantReads: 1, wantNode: "node1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			label := tt.label
			if label == "" {
				label = "1"
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns", Name: "pod",
				Labels:      map[string]string{wellknown.LabelExternalJobId: label},
				Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node1"},
				Finalizers:  []string{wellknown.FinalizerScheduler},
			}}
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
			control, err := slurmcontrol.NewControl(live, cache.Build().GetInformer(api.ObjectTypeV0044JobInfo), "", "")
			if err != nil {
				t.Fatal(err)
			}
			sb := &SlurmBridge{Client: kube, slurmControl: control}
			_, err = sb.validatePodToJob(t.Context(), pod)
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
			for _, got := range []*corev1.Pod{pod, stored} {
				if !reflect.DeepEqual(got.Labels, want.Labels) || !reflect.DeepEqual(got.Annotations, want.Annotations) || !reflect.DeepEqual(got.Finalizers, want.Finalizers) {
					t.Errorf("metadata = labels %v, annotations %v, finalizers %v; want %v, %v, %v", got.Labels, got.Annotations, got.Finalizers, want.Labels, want.Annotations, want.Finalizers)
				}
			}
		})
	}
}
