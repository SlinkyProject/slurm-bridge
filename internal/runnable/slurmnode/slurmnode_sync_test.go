// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmnode

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	slurmapi "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurmobject "github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestSlurmNodeRunnable_Sync(t *testing.T) {
	tests := []struct {
		name         string
		kubeClient   client.Client
		slurmClient  slurmclient.Client
		eventCh      chan<- event.GenericEvent
		wantErr      bool
		wantEventCnt int
	}{
		{
			name:         "empty",
			kubeClient:   fake.NewFakeClient(),
			slurmClient:  slurmclientfake.NewFakeClient(),
			eventCh:      make(chan event.GenericEvent, 10),
			wantEventCnt: 0,
		},
		{
			name: "nodes",
			kubeClient: fake.NewFakeClient(&corev1.NodeList{
				Items: []corev1.Node{
					{ObjectMeta: metav1.ObjectMeta{Name: "kube-1"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "bridge-1"}},
				},
			}),
			slurmClient: slurmclientfake.NewClientBuilder().
				WithLists(&slurmapi.V0044NodeObjectList{
					Items: []slurmapi.V0044Node{
						{Name: ptr.To("slurm-1")},
						{Name: ptr.To("bridge-1")},
					},
				}).
				Build(),
			eventCh:      make(chan event.GenericEvent, 10),
			wantEventCnt: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunnable(tt.kubeClient, tt.kubeClient, tt.slurmClient, tt.eventCh)
			gotErr := r.Sync(context.Background())
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("Sync() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("Sync() succeeded unexpectedly")
			}
			gotEventCnt := len(tt.eventCh)
			if gotEventCnt != tt.wantEventCnt {
				t.Errorf("Sync() eventCnt = %v , want = %v", gotEventCnt, tt.wantEventCnt)
			}
		})
	}
}

func TestSlurmNodeRunnable_SyncRemovesDeletedNodes(t *testing.T) {
	bridged := func(name string, state ...slurmapi.V0044NodeState) slurmapi.V0044Node {
		return slurmapi.V0044Node{
			Name:    ptr.To(name),
			State:   ptr.To(state),
			Comment: ptr.To(wellknown.SlurmNodeComment),
		}
	}
	slurmClient := slurmclientfake.NewClientBuilder().
		WithLists(&slurmapi.V0044NodeObjectList{
			Items: []slurmapi.V0044Node{
				bridged("kept", slurmapi.V0044NodeStateEXTERNAL),
				bridged("deleted", slurmapi.V0044NodeStateEXTERNAL),
				// Not external: slurmd owns its lifecycle.
				bridged("hybrid", slurmapi.V0044NodeStateIDLE),
				// External but not registered by slurm-bridge, even with the bridge feature.
				{
					Name:     ptr.To("other"),
					State:    ptr.To([]slurmapi.V0044NodeState{slurmapi.V0044NodeStateEXTERNAL}),
					Features: ptr.To(slurmapi.V0044CsvString{wellknown.SlurmFeatureGRESCompatible}),
				},
			},
		}).
		Build()
	kubeClient := fake.NewFakeClient(&corev1.NodeList{
		Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "kept"}}},
	})

	r := NewRunnable(kubeClient, kubeClient, slurmClient, make(chan event.GenericEvent, 10))
	if err := r.Sync(context.Background()); err != nil {
		t.Fatalf("Sync() failed: %v", err)
	}

	for name, wantExists := range map[string]bool{"kept": true, "deleted": false, "hybrid": true, "other": true} {
		err := slurmClient.Get(context.Background(), slurmobject.ObjectKey(name), &slurmapi.V0044Node{})
		if exists := err == nil; exists != wantExists {
			t.Errorf("Slurm node %q exists = %v, want %v (err %v)", name, exists, wantExists, err)
		}
	}
}

func TestSlurmNodeRunnable_SyncKeepsNodesWhenKubeListEmpty(t *testing.T) {
	slurmClient := slurmclientfake.NewClientBuilder().
		WithLists(&slurmapi.V0044NodeObjectList{
			Items: []slurmapi.V0044Node{{
				Name:    ptr.To("bridge-1"),
				State:   ptr.To([]slurmapi.V0044NodeState{slurmapi.V0044NodeStateEXTERNAL}),
				Comment: ptr.To(wellknown.SlurmNodeComment),
			}},
		}).
		Build()

	r := NewRunnable(fake.NewFakeClient(), fake.NewFakeClient(), slurmClient, make(chan event.GenericEvent, 10))
	if err := r.Sync(context.Background()); err != nil {
		t.Fatalf("Sync() failed: %v", err)
	}
	if err := slurmClient.Get(context.Background(), slurmobject.ObjectKey("bridge-1"), &slurmapi.V0044Node{}); err != nil {
		t.Errorf("Slurm node removed with an empty Kubernetes node list: %v", err)
	}
}

func TestSlurmNodeRunnable_SyncKeepsNodesMissingFromCache(t *testing.T) {
	bridged := func(name string) slurmapi.V0044Node {
		return slurmapi.V0044Node{
			Name:    ptr.To(name),
			State:   ptr.To([]slurmapi.V0044NodeState{slurmapi.V0044NodeStateEXTERNAL}),
			Comment: ptr.To(wellknown.SlurmNodeComment),
		}
	}
	slurmClient := slurmclientfake.NewClientBuilder().
		WithLists(&slurmapi.V0044NodeObjectList{
			Items: []slurmapi.V0044Node{bridged("cached"), bridged("by-name"), bridged("by-label"), bridged("gone")},
		}).
		Build()
	cached := fake.NewFakeClient(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cached"}})
	// The API server has Nodes the cache has not seen yet.
	live := fake.NewFakeClient(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cached"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "by-name"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   "kube-node",
			Labels: map[string]string{wellknown.LabelSlurmNodeName: "by-label"},
		}},
	)

	r := NewRunnable(cached, live, slurmClient, make(chan event.GenericEvent, 10))
	if err := r.Sync(context.Background()); err != nil {
		t.Fatalf("Sync() failed: %v", err)
	}

	for name, wantExists := range map[string]bool{"cached": true, "by-name": true, "by-label": true, "gone": false} {
		err := slurmClient.Get(context.Background(), slurmobject.ObjectKey(name), &slurmapi.V0044Node{})
		if exists := err == nil; exists != wantExists {
			t.Errorf("Slurm node %q exists = %v, want %v (err %v)", name, exists, wantExists, err)
		}
	}
}
