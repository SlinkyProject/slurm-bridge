// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"github.com/puttsk/hostlist"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmjob/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// reconcilePodMetadata will heal pod job-linkage metadata against Slurm.
// It rewrites a pod's external-job-id label when the labeled job is gone
// but another bridge job claims the pod, and clears a pod's node annotation
// when its job does not hold that node.
func (r *SlurmJobRunnable) reconcilePodMetadata(ctx context.Context) error {
	jobs, err := r.slurmControl.ListBridgeJobs(ctx)
	if err != nil {
		return err
	}

	errs := []error{}
	for _, job := range jobs {
		for _, key := range job.PodKeys {
			pod := &corev1.Pod{}
			if err := r.Get(ctx, key, pod); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				errs = append(errs, err)
				continue
			}
			if err := r.reconcileOnePodMetadata(ctx, pod, job); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (r *SlurmJobRunnable) reconcileOnePodMetadata(ctx context.Context, pod *corev1.Pod, job slurmcontrol.BridgeJob) error {
	logger := log.FromContext(ctx)

	labelVal := pod.Labels[wellknown.LabelExternalJobId]
	labeledJobId := slurmjobir.ParseSlurmJobId(labelVal)

	// Prefer the job the pod's label references while it is alive in Slurm;
	// otherwise the job claiming the pod is authoritative. Confirm with a
	// live read before correcting.
	authoritativeId := job.JobId
	authoritativeNodes := job.Nodes
	if labelVal != "" && labeledJobId != job.JobId {
		exists, nodes, err := r.slurmControl.GetJobNodesLive(ctx, labeledJobId)
		if err != nil {
			return err
		}
		if exists {
			authoritativeId = labeledJobId
			authoritativeNodes = nodes
		}
	}

	toUpdate := pod.DeepCopy()

	if labelVal != "" && labeledJobId != authoritativeId {
		logger.Info("Pod jobId label references a job Slurm no longer knows; healing",
			"pod", client.ObjectKeyFromObject(pod), "label", labelVal, "jobId", authoritativeId)
		toUpdate.Labels[wellknown.LabelExternalJobId] = strconv.Itoa(int(authoritativeId))
	}

	// The node annotation only drives scheduling; leave bound pods alone.
	node := pod.Annotations[wellknown.AnnotationExternalJobNode]
	if node != "" && pod.Spec.NodeName == "" {
		exists, liveNodes, err := r.slurmControl.GetJobNodesLive(ctx, authoritativeId)
		if err != nil {
			return err
		}
		if exists {
			authoritativeNodes = liveNodes
		}
		nodes, _ := hostlist.Expand(authoritativeNodes)
		if !exists || !slices.Contains(nodes, node) {
			logger.Info("Pod node annotation names a node its job does not hold; clearing",
				"pod", client.ObjectKeyFromObject(pod), "node", node)
			toUpdate.Annotations[wellknown.AnnotationExternalJobNode] = ""
		}
	}

	if reflect.DeepEqual(pod, toUpdate) {
		return nil
	}
	return r.Patch(ctx, toUpdate, client.StrategicMergeFrom(pod))
}
