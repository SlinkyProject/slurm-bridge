// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// membershipClient uses the selected reader while retaining the scheme needed
// to resolve legacy controller ownership.
type membershipClient struct {
	client.Client
	reader client.Reader
}

func (c membershipClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.reader.Get(ctx, key, obj, opts...)
}
func (c membershipClient) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	return c.reader.List(ctx, obj, opts...)
}

// componentMembers combines current group membership with recorded identities.
// The recorded list alone misses late arrivals; current membership alone loses
// terminating Pods when their group is deleted or recreated.
func (r *Reconciler) componentMembers(ctx context.Context, reader client.Reader, job slurmjob.RegisteredJob) ([]corev1.Pod, error) {
	info := job.Ownership()
	members := make(map[string]corev1.Pod)
	add := func(pods []corev1.Pod) {
		for _, pod := range pods {
			if pod.Spec.SchedulerName == r.schedulerName {
				members[externaljobinfo.PodKey(&pod)] = pod
			}
		}
	}
	if info.PodGroup != nil {
		pods, _, err := r.membersFrom(ctx, reader, groupKey(*info.PodGroup))
		if err != nil && !errors.Is(err, slurmjobir.ErrorPodGroupNoPods) {
			return nil, err
		}
		add(pods)
	}
	for _, key := range info.Pods {
		ref := externaljobinfo.ParsePodKey(key)
		pod := &corev1.Pod{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if ref.UID != "" && ref.UID != pod.UID {
			continue
		}
		add([]corev1.Pod{*pod})
		// Older records have only Pod identities. Resolve their current group while
		// an original member still exists, including a legacy LWS's late workers.
		if info.PodGroup == nil && pod.Spec.SchedulerName == r.schedulerName {
			root, err := slurmjobir.WorkloadRootForPod(membershipClient{Client: r.Client, reader: reader}, r.workloadAPI, ctx, pod)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			current := allocationKey(pod, *root)
			if current.Kind != "Pod" {
				pods, _, err := r.membersFrom(ctx, reader, current)
				if err != nil && !errors.Is(err, slurmjobir.ErrorPodGroupNoPods) && !errors.Is(err, slurmjob.ErrLWSLeaderMissing) {
					return nil, err
				}
				add(pods)
			}
		}
	}
	// Keep published members visible even after their owning group is removed.
	// Labels are only evidence to retain an allocation, never to adopt ownership.
	labeled := &corev1.PodList{}
	if err := reader.List(ctx, labeled, client.MatchingLabels{wellknown.LabelExternalJobId: strconv.Itoa(int(job.JobId))}); err != nil {
		return nil, err
	}
	add(labeled.Items)
	result := make([]corev1.Pod, 0, len(members))
	for _, pod := range members {
		result = append(result, pod)
	}
	return result, nil
}

func needsAllocation(pods []corev1.Pod) bool {
	for i := range pods {
		if !podReadyForCleanup(&pods[i]) {
			return true
		}
	}
	return false
}

// liveJob is used only at destructive boundaries. Missing jobs are released;
// unknown states and transport errors must retain finalizers and claims.
func (r *Reconciler) liveJob(ctx context.Context, id int32) (*api.V0044JobInfo, error) {
	raw := &api.V0044JobInfo{}
	key := object.ObjectKey(strconv.Itoa(int(id)))
	if err := r.slurmClient.Get(ctx, key, raw, &slurmclient.GetOptions{SkipCache: true}); err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return nil, nil //nolint:nilnil // An absent allocation is a confirmed release.
		}
		return nil, err
	}
	if raw.GetKey() != key {
		return nil, fmt.Errorf("slurm returned job %s for %s", raw.GetKey(), key)
	}
	return raw, nil
}

// Slurm can report a terminal state alongside COMPLETING while epilog cleanup still
// holds resources. Keep Kubernetes claims until that allocation is released.
func allocationReleased(raw *api.V0044JobInfo) (bool, error) {
	job, err := slurmjob.FromJobInfo(raw)
	return job.Finished && !slices.Contains(ptr.Deref(raw.JobState, nil), api.V0044JobInfoJobStateCOMPLETING), err
}

func (r *Reconciler) releaseComponent(ctx context.Context, cached slurmjob.RegisteredJob) (bool, error) {
	raw, err := r.liveJob(ctx, cached.JobId)
	if err != nil || raw == nil {
		return raw == nil && err == nil, err
	}
	live, err := slurmjob.FromJobInfo(raw)
	if err != nil {
		return false, err
	}
	if live.Finished {
		return allocationReleased(raw)
	}
	var info externaljobinfo.ExternalJobInfo
	if err := externaljobinfo.ParseIntoExternalJobInfo(raw.AdminComment, &info); err != nil {
		return false, err
	}
	if live.AllocationID() != cached.AllocationID() || live.HetJobOffset != cached.HetJobOffset || !reflect.DeepEqual(info, cached.Ownership()) {
		return false, fmt.Errorf("ownership changed for job %d", cached.JobId)
	}
	members, err := r.componentMembers(ctx, r.apiReader, cached)
	if err != nil || needsAllocation(members) {
		return false, err
	}
	if live.HetJobId != 0 && live.Pending {
		// A pending component cannot be canceled independently. Check every current
		// root member as well as every component before canceling the whole hetjob.
		if info.CompositePodGroup != nil {
			pods, _, err := r.membersFrom(ctx, r.apiReader, groupKey(*info.CompositePodGroup))
			if err != nil && !errors.Is(err, slurmjobir.ErrorPodGroupNoPods) {
				return false, err
			}
			if needsAllocation(pods) {
				return false, nil
			}
		}
		// The cache may not contain every component yet. A whole-allocation
		// cancellation must account for all live component records as well.
		all := &api.V0044JobInfoObjectList{}
		if err := r.slurmClient.List(ctx, all, &slurmclient.ListOptions{SkipCache: true}); err != nil {
			return false, err
		}
		for _, raw := range all.Items {
			if raw.HetJobId == nil || ptr.Deref(raw.HetJobId.Number, 0) != live.HetJobId {
				continue
			}
			sibling, err := slurmjob.RegisteredJobFromInfo(&raw)
			if err != nil {
				return false, err
			}
			pods, err := r.componentMembers(ctx, r.apiReader, sibling)
			if err != nil || needsAllocation(pods) {
				return false, err
			}
		}
	}
	if live.HetJobId != 0 && !live.Pending {
		err = r.slurmControl.TerminateHetJobComponent(ctx, live.HetJobId, live.HetJobOffset)
	} else {
		err = r.slurmControl.TerminateJob(ctx, live.AllocationID())
	}
	if err != nil {
		return false, err
	}
	// A cancellation acknowledgement is not proof that nodes have been released.
	raw, err = r.liveJob(ctx, cached.JobId)
	if err != nil || raw == nil {
		return raw == nil && err == nil, err
	}
	return allocationReleased(raw)
}

func (r *Reconciler) reconcileLifecycle(ctx context.Context, key Request, pods []corev1.Pod, jobs []slurmjob.RegisteredJob) (bool, error) {
	if len(jobs) == 0 && r.recent != nil {
		if value, cooling := r.recent.Get(key); cooling {
			id := value.(int32)
			if id == 0 {
				return true, nil
			}
			raw, err := r.liveJob(ctx, id)
			if err != nil {
				return false, err
			}
			if raw != nil {
				released, err := allocationReleased(raw)
				if err != nil {
					return false, err
				}
				if !released {
					return true, nil
				}
			}
		}
	}
	members := make(map[string]corev1.Pod)
	for _, pod := range pods {
		members[externaljobinfo.PodKey(&pod)] = pod
	}
	released := make(map[int32]bool)
	associatedJobs := make(map[string][]int32)
	waiting := false
	for _, job := range jobs {
		current, err := r.componentMembers(ctx, r.cachedMembers(), job)
		if err != nil {
			return false, err
		}
		for _, pod := range current {
			members[externaljobinfo.PodKey(&pod)] = pod
			associatedJobs[externaljobinfo.PodKey(&pod)] = append(associatedJobs[externaljobinfo.PodKey(&pod)], job.JobId)
		}
		if needsAllocation(current) && !job.Finished {
			continue
		}
		released[job.JobId], err = r.releaseComponent(ctx, job)
		if err != nil {
			return false, err
		}
		if !released[job.JobId] && !needsAllocation(current) {
			waiting = true
		}
	}
	for _, pod := range members {
		if pod.Spec.SchedulerName != r.schedulerName {
			continue
		}
		req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pod)}
		if !podReadyForCleanup(&pod) {
			if hasRegistrationGate(&pod) {
				continue
			}
			changed, err := r.syncPod(ctx, req)
			if err != nil {
				return false, err
			}
			if changed {
				waiting = true
				continue
			}
			continue
		}
		// Keep a terminal member's identity until all allocations associated with it
		// have released. A cache miss, including after restart, is not confirmation.
		safe := true
		for _, id := range associatedJobs[externaljobinfo.PodKey(&pod)] {
			if !released[id] {
				safe = false
			}
		}
		if !safe {
			continue
		}
		id := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
		if id > 0 && !released[id] {
			raw, err := r.liveJob(ctx, id)
			if err != nil {
				return false, err
			}
			if raw != nil {
				released, err := allocationReleased(raw)
				if err != nil {
					return false, err
				}
				if !released {
					waiting = true
					continue
				}
			}
		}
		if err := r.prepareTerminalPod(ctx, req, &pod); err != nil {
			return false, err
		}
	}
	return waiting, nil
}
