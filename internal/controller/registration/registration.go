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
	if slices.ContainsFunc(jobs, func(job slurmjob.RegisteredJob) bool { return !job.Finished }) {
		r.recent.Delete(key)
		components, complete, err := slurmjob.MatchAllocationComponents(ir, jobs)
		if err != nil || !complete {
			return waiting, err
		}
		return reconcile.Result{}, r.publish(ctx, ir, components)
	}
	var cooldown *int32
	if previous, cooling := r.recent.Get(key); cooling {
		id := previous.(int32)
		cooldown = &id
	}
	if !submissionAllowed(jobs, cooldown, ir.AllPods(), r.eligible) {
		return waiting, nil
	}
	ready, err := slurmjobir.RegistrationReady(r.Client, r.registry, r.workloadAPI, ctx, pod, ir)
	if err != nil {
		return registrationReadResult(err)
	}
	if !ready {
		return waiting, nil
	}
	id, err := r.submit.RegisterJob(ctx, ir)
	if errors.Is(err, schedulercontrol.ErrRegistrationNotSubmitted) {
		r.recent.Delete(key)
	} else {
		r.recent.Set(key, id, registrationCooldown)
	}
	return waiting, err
}

func (r *Reconciler) publish(ctx context.Context, ir *slurmjobir.SlurmJobIR, components []slurmjob.ExternalJob) error {
	var errs []error
	for i, component := range ir.Components {
		job := components[i]
		if job.Finished {
			continue
		}
		for _, member := range component.Pods.Items {
			if !r.eligible(&member) {
				continue
			}
			updated := slurmjob.PodWithMetadata(ctx, &member, job, true)
			updated.Spec.SchedulingGates = slices.DeleteFunc(updated.Spec.SchedulingGates, func(g corev1.PodSchedulingGate) bool { return g.Name == wellknown.SchedulingGateRegistration })
			if !reflect.DeepEqual(&member, updated) {
				errs = append(errs, client.IgnoreNotFound(r.Patch(ctx, updated, client.StrategicMergeFrom(&member, client.MergeFromWithOptimisticLock{}))))
			}
		}
	}
	return errors.Join(errs...)
}
