// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	slurmapi "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurminterceptor "github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	slurmobject "github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"
)

func TestSlurmJobRunnable_Sync(t *testing.T) {
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
			name: "pods and jobs",
			kubeClient: fake.NewFakeClient(&corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo-1",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "1",
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo-2",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "1",
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "bar-1",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "2",
							},
						},
					},
				},
			}),
			slurmClient: slurmclientfake.NewClientBuilder().
				WithLists(&slurmtypes.V0044JobInfoList{
					Items: []slurmtypes.V0044JobInfo{
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](1),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/foo-1", "default/foo-2"},
								}).ToString()),
							},
						},
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](2),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/bar-1"},
								}).ToString()),
							},
						},
					},
				}).
				Build(),
			eventCh:      make(chan event.GenericEvent, 10),
			wantEventCnt: 3,
		},
		{
			name: "no pods for job",
			kubeClient: fake.NewFakeClient(&corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "bar-1",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "2",
							},
						},
					},
				},
			}),
			slurmClient: slurmclientfake.NewClientBuilder().
				WithLists(&slurmtypes.V0044JobInfoList{
					Items: []slurmtypes.V0044JobInfo{
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](1),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/foo-1", "default/foo-2"},
								}).ToString()),
							},
						},
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](2),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/bar-1"},
								}).ToString()),
							},
						},
					},
				}).
				Build(),
			eventCh:      make(chan event.GenericEvent, 10),
			wantEventCnt: 3,
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

func TestSlurmJobRunnable_cleanDanglingJob(t *testing.T) {
	callCnt := 0
	tests := []struct {
		name        string
		kubeClient  client.Client
		slurmClient slurmclient.Client
		jobId       int32
		wantErr     bool
		wantDelete  bool
	}{
		{
			name:        "smoke",
			kubeClient:  fake.NewFakeClient(),
			slurmClient: slurmclientfake.NewFakeClient(),
			wantDelete:  false,
		},
		{
			name: "pods and jobs",
			kubeClient: fake.NewFakeClient(&corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo-1",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "1",
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo-2",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "1",
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "bar-1",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "2",
							},
						},
					},
				},
			}),
			slurmClient: slurmclientfake.NewClientBuilder().
				WithLists(&slurmtypes.V0044JobInfoList{
					Items: []slurmtypes.V0044JobInfo{
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](1),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/foo-1", "default/foo-2"},
								}).ToString()),
							},
						},
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](2),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/bar-1"},
								}).ToString()),
							},
						},
					},
				}).
				Build(),
			jobId:      1,
			wantDelete: false,
		},
		{
			name: "no pods for job",
			kubeClient: fake.NewFakeClient(&corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "bar-1",
							Namespace: corev1.NamespaceDefault,
							Labels: map[string]string{
								wellknown.LabelPlaceholderJobId: "2",
							},
						},
					},
				},
			}),
			slurmClient: slurmclientfake.NewClientBuilder().
				WithLists(&slurmtypes.V0044JobInfoList{
					Items: []slurmtypes.V0044JobInfo{
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](1),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/foo-1", "default/foo-2"},
								}).ToString()),
							},
						},
						{
							V0044JobInfo: slurmapi.V0044JobInfo{
								JobId: ptr.To[int32](2),
								AdminComment: ptr.To((&placeholderinfo.PlaceholderInfo{
									Pods: []string{"default/bar-1"},
								}).ToString()),
							},
						},
					},
				}).
				WithInterceptorFuncs(slurminterceptor.Funcs{
					Delete: func(ctx context.Context, obj slurmobject.Object, opts ...slurmclient.DeleteOption) error {
						callCnt++
						return nil
					},
				}).
				Build(),
			jobId:      1,
			wantDelete: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunnable(tt.kubeClient, tt.slurmClient, nil)
			callCnt = 0
			gotErr := r.cleanDanglingJob(context.Background(), tt.jobId)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("cleanDanglingJob() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("cleanDanglingJob() succeeded unexpectedly")
			}
			if tt.wantDelete && callCnt == 0 {
				t.Error("cleanDanglingJob() want delete, but no delete called")
			}
		})
	}
}
