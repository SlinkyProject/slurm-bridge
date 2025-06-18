// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

var (
	// Ref: https://lws.sigs.k8s.io/docs/
	lws_v1 = metav1.TypeMeta{APIVersion: "leaderworkerset.x-k8s.io/v1", Kind: "LeaderWorkerSet"}

	ErrorLWSCouldNotGet = errors.New("could not get leaderworkerset")
	ErrorLWSNoPods      = errors.New("no pods for LWS group found")
)

// PreFilter performs LeaderWorkerSet specific PreFilter functions
func (t *translator) PreFilterLWS(pod *corev1.Pod, slurmJobIR *SlurmJobIR) *framework.Status {
	lws := lwsv1.LeaderWorkerSet{}
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.GetNamespace(), Name: slurmJobIR.RootPOM.GetName()}
	if err := t.Get(t.ctx, key, &lws); err != nil {
		return framework.NewStatus(framework.Error, ErrorLWSCouldNotGet.Error())
	}

	// Determine if there are enough LWS pods for the group
	if int32(len(slurmJobIR.Pods.Items)) < *lws.Spec.LeaderWorkerTemplate.Size { //nolint:gosec
		if pod.Labels[wellknown.LabelPlaceholderJobId] == "" {
			return framework.NewStatus(framework.Error, ErrorInsuffientPods.Error())
		} else {
			return framework.NewStatus(framework.Error, ErrorPlaceholderJobInvalid.Error())
		}
	}
	return framework.NewStatus(framework.Success)
}

// fromLws will translate a pod from a LeaderWorkerSet into a SlurmJobIR.
func (t *translator) fromLws(pod *corev1.Pod, rootPOM *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
	lws := lwsv1.LeaderWorkerSet{}
	key := client.ObjectKey{Namespace: rootPOM.GetNamespace(), Name: rootPOM.GetName()}
	if err := t.Get(t.ctx, key, &lws); err != nil {
		return nil, err
	}

	slurmJobIR := &SlurmJobIR{}
	// From the current pod's annotations we can construct the list
	// of pods that belong to this LWS group.
	if err := t.List(t.ctx, &slurmJobIR.Pods,
		&client.ListOptions{LabelSelector: labels.SelectorFromSet(
			labels.Set{lwsv1.GroupUniqueHashLabelKey: pod.Labels[lwsv1.GroupUniqueHashLabelKey]},
		)}); err != nil {
		return nil, err
	}

	if len(slurmJobIR.Pods.Items) == 0 {
		return nil, ErrorLWSNoPods
	}

	slurmJobIR.JobInfo.JobName = ptr.To(pod.Labels[lwsv1.SetNameLabelKey] + "-" + pod.Labels[lwsv1.GroupIndexLabelKey])
	slurmJobIR.JobInfo.MaxNodes = ptr.To(int32(*lws.Spec.LeaderWorkerTemplate.Size))
	slurmJobIR.JobInfo.MinNodes = ptr.To(int32(*lws.Spec.LeaderWorkerTemplate.Size))
	slurmJobIR.JobInfo.TasksPerNode = ptr.To(int32(1))

	return slurmJobIR, nil
}
