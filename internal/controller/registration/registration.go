// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	podv1 "k8s.io/kubernetes/pkg/api/v1/pod"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/SlinkyProject/slurm-bridge/internal/config"
	schedulercontrol "github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Submission is serialized by the allocation queue. This memo only suppresses
// retries while the Slurm informer catches up, including after a lost response.
const registrationCooldown = 2 * config.SlurmCacheSyncPeriod
const registrationRetry = 2 * time.Second
const registrationDependencyRetry = 5 * time.Second

func hasRegistrationGate(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool { return g.Name == wellknown.SchedulingGateRegistration })
}

func (r *Reconciler) eligible(pod *corev1.Pod) bool {
	return pod.Spec.SchedulerName == r.schedulerName && pod.Spec.NodeName == "" && pod.DeletionTimestamp == nil && !podv1.IsPodTerminal(pod)
}

// submissionAllowed reports whether a new submission may be attempted now.
func submissionAllowed(jobs []slurmjob.RegisteredJob, cooldown *int32, members []corev1.Pod, eligible func(*corev1.Pod) bool) bool {
	for _, member := range members {
		if !eligible(&member) {
			return false
		}
	}
	// A known submission first observed as terminal can be replaced immediately.
	return cooldown == nil || (*cooldown != 0 && slices.ContainsFunc(jobs, func(job slurmjob.RegisteredJob) bool { return job.AllocationID() == *cooldown }))
}

func (r *Reconciler) register(ctx context.Context, key Request, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR, jobs []slurmjob.RegisteredJob) (reconcile.Result, error) {
	waiting := reconcile.Result{RequeueAfter: registrationRetry}
	var cooldown *int32
	if previous, cooling := r.recent.Get(key); cooling {
		id := previous.(int32)
		cooldown = &id
	}
	if !submissionAllowed(jobs, cooldown, ir.AllPods(), r.eligible) {
		return waiting, nil
	}
	ready, message, err := slurmjobir.RegistrationReady(r.cachedMembers(), r.registry, r.workloadAPI, ctx, pod, ir)
	if err != nil {
		return r.registrationReadEvent(key, err)
	}
	if !ready {
		r.registrationEvent(key, corev1.EventTypeNormal, "WaitingForMembers", message)
		return waiting, nil
	}
	id, err := r.submit.RegisterJob(ctx, ir)
	if errors.Is(err, schedulercontrol.ErrRegistrationNotSubmitted) {
		r.recent.Delete(key)
	} else {
		r.recent.Set(key, id, registrationCooldown)
	}
	switch kind, retry := classifyRejection(err); {
	case err == nil:
		r.clearRegistrationEvent(key)
	case kind == rejectRequest:
		r.registrationEvent(key, corev1.EventTypeWarning, "SlurmRegistrationRejected", err.Error())
		return reconcile.Result{RequeueAfter: retry}, nil
	case kind == rejectLimit:
		r.registrationEvent(key, corev1.EventTypeNormal, "WaitingForSlurm", err.Error())
		return reconcile.Result{RequeueAfter: retry}, nil
	default:
		r.registrationEvent(key, corev1.EventTypeWarning, "SlurmRegistrationFailed", err.Error())
	}
	return waiting, err
}

// publishRegistered uses the submission records directly; current group shape
// and resource requests matter only when submitting the next batch.
func (r *Reconciler) publishRegistered(ctx context.Context, key Request, pods []corev1.Pod, jobs []slurmjob.RegisteredJob) (reconcile.Result, error) {
	membership := slurmjob.IndexAllocationPods(jobs)
	selected := make(map[client.ObjectKey]bool, len(pods))
	var errs []error
	waiting := false
	for _, job := range jobs {
		if job.Finished {
			continue
		}
		var members []corev1.Pod
		for _, pod := range pods {
			if membership.Contains(&pod, job.JobId) {
				members = append(members, pod)
				selected[client.ObjectKeyFromObject(&pod)] = true
			}
		}
		errs = append(errs, r.publish(ctx, members, job.ExternalJob))
	}
	for _, pod := range pods {
		if r.eligible(&pod) && !selected[client.ObjectKeyFromObject(&pod)] {
			waiting = true
		}
	}
	result := reconcile.Result{}
	if waiting {
		r.registrationEvent(key, corev1.EventTypeNormal, "WaitingForNextAllocation",
			"The submitted Slurm allocation has fixed membership; later Pods and child groups must wait for it to finish and for a new batch to meet quorum")
		result.RequeueAfter = registrationRetry
	} else {
		r.clearRegistrationEvent(key)
	}
	return result, errors.Join(errs...)
}

func (r *Reconciler) publish(ctx context.Context, members []corev1.Pod, job slurmjob.ExternalJob) error {
	var errs []error
	for _, member := range members {
		if !r.eligible(&member) {
			continue
		}
		updated, err := r.podWithMetadata(ctx, &member, job, true)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		updated.Spec.SchedulingGates = slices.DeleteFunc(updated.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool { return g.Name == wellknown.SchedulingGateRegistration })
		if !reflect.DeepEqual(&member, updated) {
			errs = append(errs, client.IgnoreNotFound(r.Patch(ctx, updated, client.StrategicMergeFrom(&member, client.MergeFromWithOptimisticLock{}))))
		}
	}
	return errors.Join(errs...)
}
