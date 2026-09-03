// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

var (
	// Ref: https://lws.sigs.k8s.io/docs/
	lws_v1 = metav1.TypeMeta{APIVersion: "leaderworkerset.x-k8s.io/v1", Kind: "LeaderWorkerSet"}

	ErrorLWSCouldNotGet = errors.New("could not get leaderworkerset")
	ErrorLWSNoPods      = errors.New("no pods for LWS group found")
)

// PreFilter performs LeaderWorkerSet specific PreFilter functions
func (t *translator) PreFilterLWS(pod *corev1.Pod, slurmJobIR *SlurmJobIR) *fwk.Status {
	lws := lwsv1.LeaderWorkerSet{}
	key := client.ObjectKey{Namespace: slurmJobIR.RootPOM.GetNamespace(), Name: slurmJobIR.RootPOM.GetName()}
	if err := t.Get(t.ctx, key, &lws); err != nil {
		return fwk.NewStatus(fwk.Error, ErrorLWSCouldNotGet.Error())
	}

	// Determine if there are enough LWS pods for the group
	if int32(len(slurmJobIR.AllPods())) < *lws.Spec.LeaderWorkerTemplate.Size { //nolint:gosec
		if pod.Labels[wellknown.LabelExternalJobId] == "" {
			return fwk.NewStatus(fwk.Error, ErrorInsuffientPods.Error())
		} else {
			return fwk.NewStatus(fwk.Error, ErrorExternalJobInvalid.Error())
		}
	}
	return fwk.NewStatus(fwk.Success)
}

// fromLws will translate a pod from a LeaderWorkerSet into a SlurmJobIR.
func (t *translator) fromLws(pod *corev1.Pod, rootPOM *metav1.PartialObjectMetadata) (*SlurmJobIR, error) {
	lws := lwsv1.LeaderWorkerSet{}
	key := client.ObjectKey{Namespace: rootPOM.GetNamespace(), Name: rootPOM.GetName()}
	if err := t.Get(t.ctx, key, &lws); err != nil {
		return nil, err
	}

	size := ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1)

	slurmJobIR := &SlurmJobIR{}

	// List all pods in the LWS group before splitting leaders from workers.
	var groupPods corev1.PodList
	if err := t.List(t.ctx, &groupPods,
		&client.ListOptions{
			LabelSelector: labels.SelectorFromSet(
				labels.Set{lwsv1.GroupUniqueHashLabelKey: pod.Labels[lwsv1.GroupUniqueHashLabelKey]},
			),
			Namespace: rootPOM.GetNamespace()},
	); err != nil {
		return nil, err
	}

	if len(groupPods.Items) == 0 {
		return nil, ErrorLWSNoPods
	}

	component := SlurmJobComponent{
		Pods: groupPods,
		JobInfo: SlurmJobIRJobInfo{
			JobName:      ptr.To(pod.Labels[lwsv1.SetNameLabelKey] + "-" + pod.Labels[lwsv1.GroupIndexLabelKey]),
			MinNodes:     ptr.To(size),
			MaxNodes:     ptr.To(size),
			TasksPerNode: ptr.To(int32(1)),
		},
	}
	slurmJobIR.Components = []SlurmJobComponent{component}
	return slurmJobIR, nil
}
