// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	podv1 "k8s.io/kubernetes/pkg/api/v1/pod"

	"github.com/SlinkyProject/slurm-client/pkg/hostlist"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// PodWithMetadata returns a copy with job identity and placement metadata repaired.
func PodWithMetadata(ctx context.Context, pod *corev1.Pod, job ExternalJob, adopt bool) *corev1.Pod {
	logger := klog.FromContext(ctx)

	toUpdate := pod.DeepCopy()

	hasJobID := pod.Labels[wellknown.LabelExternalJobId] != ""
	if (hasJobID || adopt) && !job.Finished {
		if toUpdate.Labels == nil {
			toUpdate.Labels = make(map[string]string)
		}
		validateIDLabel(ctx, job.JobId, wellknown.LabelExternalJobId, pod, toUpdate)
		validateIDLabel(ctx, job.HetJobId, wellknown.LabelExternalHetJobId, pod, toUpdate)
		if job.HetJobId > 0 && job.HetJobOffset >= 0 {
			toUpdate.Labels[wellknown.LabelExternalHetJobOffset] = strconv.FormatInt(int64(job.HetJobOffset), 10)
		} else {
			delete(toUpdate.Labels, wellknown.LabelExternalHetJobOffset)
		}

		if pod.DeletionTimestamp == nil && !podv1.IsPodTerminal(pod) &&
			!slices.Contains(toUpdate.Finalizers, wellknown.FinalizerScheduler) {
			toUpdate.Finalizers = append(
				toUpdate.Finalizers,
				wellknown.FinalizerScheduler,
			)
		}
	}

	// Only unbound Pods use the node annotation for placement.
	// A pending cache record may predate the scheduler's placement decision.
	nodes, _ := hostlist.Expand(job.Nodes)
	if pod.Spec.NodeName == "" && pod.Annotations[wellknown.AnnotationExternalJobNode] != "" &&
		(job.Finished || !job.Pending && !slices.Contains(nodes, pod.Annotations[wellknown.AnnotationExternalJobNode])) {
		logger.V(3).Info("Pod node annotation does not match Slurm nodes", "pod", klog.KObj(pod),
			"node annotation", pod.Annotations[wellknown.AnnotationExternalJobNode],
			"slurm job id", job.JobId)
		toUpdate.Annotations[wellknown.AnnotationExternalJobNode] = ""
	}
	return toUpdate
}

func validateIDLabel(ctx context.Context, id int32, label string, pod *corev1.Pod, newPod *corev1.Pod) {
	logger := klog.FromContext(ctx)

	currentLabel, labelPresent := pod.Labels[label]
	currentID := slurmjobir.ParseSlurmJobId(currentLabel)

	if id > 0 {
		if currentID != id {
			logger.V(3).Info("Updating pod label to match Slurm job info", "pod", klog.KObj(pod),
				"label", label,
				"label contents", pod.Labels[label],
				"slurm job id", id)
			newPod.Labels[label] = strconv.Itoa(int(id))
		}
	} else if labelPresent {
		logger.V(3).Info("Deleting invalid label from pod",
			"label", label,
			"label contents", currentLabel,
		)
		delete(newPod.Labels, label)
	}
}
