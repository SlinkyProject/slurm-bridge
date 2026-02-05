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
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"
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
				WithLists(&slurmtypes.V0044NodeList{
					Items: []slurmtypes.V0044Node{
						{V0044Node: slurmapi.V0044Node{Name: ptr.To("slurm-1")}},
						{V0044Node: slurmapi.V0044Node{Name: ptr.To("bridge-1")}},
					},
				}).
				Build(),
			eventCh:      make(chan event.GenericEvent, 10),
			wantEventCnt: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunnable(tt.kubeClient, tt.slurmClient, tt.eventCh)
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
