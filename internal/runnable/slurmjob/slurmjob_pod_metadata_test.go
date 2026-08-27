// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slurmapi "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurminterceptor "github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	slurmobject "github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmjob/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func bridgeJob(t *testing.T, id int32, nodes string, state slurmapi.V0044JobInfoJobState, pods ...string) slurmtypes.V0044JobInfo {
	t.Helper()
	extInfo := externaljobinfo.ExternalJobInfo{Pods: pods}
	return slurmtypes.V0044JobInfo{V0044JobInfo: slurmapi.V0044JobInfo{
		JobId:        ptr.To(id),
		Nodes:        ptr.To(nodes),
		JobState:     &[]slurmapi.V0044JobInfoJobState{state},
		AdminComment: ptr.To(extInfo.ToString()),
	}}
}

func metadataPod(name, jobId, nodeAnnotation, boundNode string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: corev1.NamespaceDefault,
	}}
	if jobId != "" {
		p.Labels = map[string]string{wellknown.LabelExternalJobId: jobId}
	}
	if nodeAnnotation != "" {
		p.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: nodeAnnotation}
	}
	p.Spec.NodeName = boundNode
	return p
}

func TestSlurmJobRunnable_reconcilePodMetadata(t *testing.T) {
	tests := []struct {
		name           string
		pod            *corev1.Pod
		jobs           []slurmtypes.V0044JobInfo
		wantLabel      string
		wantAnnotation string
	}{
		{
			name: "label rewritten when labeled job is gone",
			pod:  metadataPod("pod1", "999", "", ""),
			jobs: []slurmtypes.V0044JobInfo{
				bridgeJob(t, 7, "", slurmapi.V0044JobInfoJobStatePENDING, "default/pod1"),
			},
			wantLabel: "7",
		},
		{
			name: "label preserved while labeled job is alive",
			pod:  metadataPod("pod1", "8", "", ""),
			jobs: []slurmtypes.V0044JobInfo{
				bridgeJob(t, 7, "", slurmapi.V0044JobInfoJobStatePENDING, "default/pod1"),
				bridgeJob(t, 8, "", slurmapi.V0044JobInfoJobStatePENDING, "default/pod1-other"),
			},
			wantLabel: "8",
		},
		{
			name: "annotation cleared when the job does not hold the node",
			pod:  metadataPod("pod1", "7", "node9", ""),
			jobs: []slurmtypes.V0044JobInfo{
				bridgeJob(t, 7, "node1", slurmapi.V0044JobInfoJobStateRUNNING, "default/pod1"),
			},
			wantLabel:      "7",
			wantAnnotation: "",
		},
		{
			name: "annotation preserved when the job holds the node",
			pod:  metadataPod("pod1", "7", "node1", ""),
			jobs: []slurmtypes.V0044JobInfo{
				bridgeJob(t, 7, "node1", slurmapi.V0044JobInfoJobStateRUNNING, "default/pod1"),
			},
			wantLabel:      "7",
			wantAnnotation: "node1",
		},
		{
			name: "bound pod annotation left alone",
			pod:  metadataPod("pod1", "7", "node9", "kube-node-9"),
			jobs: []slurmtypes.V0044JobInfo{
				bridgeJob(t, 7, "node1", slurmapi.V0044JobInfoJobStateRUNNING, "default/pod1"),
			},
			wantLabel:      "7",
			wantAnnotation: "node9",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeClient := fake.NewFakeClient(tt.pod)
			jobList := &slurmtypes.V0044JobInfoList{Items: tt.jobs}
			slurmClient := slurmclientfake.NewClientBuilder().WithLists(jobList).Build()
			r := &SlurmJobRunnable{
				Client:       kubeClient,
				slurmControl: slurmcontrol.NewControl(slurmClient),
			}

			if err := r.reconcilePodMetadata(context.Background()); err != nil {
				t.Fatalf("reconcilePodMetadata() error = %v", err)
			}

			got := &corev1.Pod{}
			if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(tt.pod), got); err != nil {
				t.Fatal(err)
			}
			if got.Labels[wellknown.LabelExternalJobId] != tt.wantLabel {
				t.Errorf("label = %q, want %q", got.Labels[wellknown.LabelExternalJobId], tt.wantLabel)
			}
			if got.Annotations[wellknown.AnnotationExternalJobNode] != tt.wantAnnotation {
				t.Errorf("annotation = %q, want %q", got.Annotations[wellknown.AnnotationExternalJobNode], tt.wantAnnotation)
			}
		})
	}
}

// A live read of a missing job returns slurmrestd's raw not-found error
// ("Not Found\nInvalid job id specified") rather than the typed
// ErrObjectNotFound; the reconciler must still heal the pod.
func TestSlurmJobRunnable_reconcilePodMetadata_RawNotFound(t *testing.T) {
	pod := metadataPod("pod1", "999", "", "")
	jobList := &slurmtypes.V0044JobInfoList{Items: []slurmtypes.V0044JobInfo{
		bridgeJob(t, 7, "", slurmapi.V0044JobInfoJobStatePENDING, "default/pod1"),
	}}
	intercept := slurminterceptor.Funcs{
		Get: func(ctx context.Context, key slurmobject.ObjectKey, obj slurmobject.Object, opts ...slurmclient.GetOption) error {
			if string(key) == "999" {
				// The realistic shape: an aggregate of status text plus
				// slurmrestd's message.
				return utilerrors.NewAggregate([]error{errors.New("Not Found"), errors.New("Invalid job id specified")})
			}
			return nil
		},
	}
	slurmClient := slurmclientfake.NewClientBuilder().WithLists(jobList).WithInterceptorFuncs(intercept).Build()
	kubeClient := fake.NewFakeClient(pod)
	r := &SlurmJobRunnable{
		Client:       kubeClient,
		slurmControl: slurmcontrol.NewControl(slurmClient),
	}
	if err := r.reconcilePodMetadata(context.Background()); err != nil {
		t.Fatalf("reconcilePodMetadata() error = %v", err)
	}
	got := &corev1.Pod{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[wellknown.LabelExternalJobId] != "7" {
		t.Errorf("label = %q, want healed to \"7\"", got.Labels[wellknown.LabelExternalJobId])
	}
}
