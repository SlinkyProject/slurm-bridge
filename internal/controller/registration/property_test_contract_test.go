// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"

	api "github.com/SlinkyProject/slurm-client/api/v0044"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/controllercheck"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Contract for Pod, Job, native PodGroup, composite heterogeneous allocation,
// and LWS replica lifecycles. Each LWS replica has an independent allocation.
// Cancellation retains Slurm's terminal record, so ownership means a record in
// authoritative Slurm state, not necessarily an unfinished job. Records are not
// purged while Kubernetes still refers to them.
//
// Recovery stops new workloads, external transitions and faults, fully advances
// the informer, marks it synced, and fairly drains keys past the cooldown. Pods
// must retain quorum for convergence. A running, partially bound allocation
// must still register its unbound members. If a bound member's job is canceled,
// convergence cannot require resubmission: the controller must refuse it and this
// world does not perform the Pod controller's cleanup. Missing members are not
// replaced. Intent records preserve the original component membership.
// Safety uses committed effects and authoritative records, never the current cache.
var registrationContract = controllercheck.Contract[registrationState]{
	Always: []controllercheck.Rule[registrationState]{
		{Name: "label-ownership", Statement: "every job label names a Slurm record listing that Pod UID", Check: func(s registrationState) error {
			for _, pod := range s.Pods {
				label := pod.Labels[wellknown.LabelExternalJobId]
				if label == "" {
					continue
				}
				owned := false
				for _, job := range s.Jobs {
					if strconv.FormatInt(int64(*job.JobId), 10) != label {
						continue
					}
					info, err := worldIdentity(job)
					if err != nil {
						return err
					}
					owned = slices.Contains(info.Pods, externaljobinfo.PodKey(&pod))
				}
				if !owned {
					return fmt.Errorf("Pod %s UID %s labels unowned job %s", pod.Name, pod.UID, label)
				}
			}
			return nil
		}},
		{Name: "gate-publication", Statement: "gate removal and allocation label publication are one patch", Check: func(s registrationState) error {
			for _, e := range s.Effects {
				if e.Operation != "pod.patch" {
					continue
				}
				if hasRegistrationGate(e.BeforePod) && !hasRegistrationGate(e.AfterPod) {
					label := e.AfterPod.Labels[wellknown.LabelExternalJobId]
					if label == "" || label == e.BeforePod.Labels[wellknown.LabelExternalJobId] {
						return fmt.Errorf("Pod %s lost gate without publishing its label", e.AfterPod.Name)
					}
				}
				for _, gate := range e.BeforePod.Spec.SchedulingGates {
					if gate.Name != wellknown.SchedulingGateRegistration && !slices.Contains(e.AfterPod.Spec.SchedulingGates, gate) {
						return fmt.Errorf("Pod %s lost another actor's gate", e.AfterPod.Name)
					}
				}
			}
			return nil
		}},
		{Name: "bound-publication", Statement: "the controller never patches a bound Pod", Check: func(s registrationState) error {
			for _, e := range s.Effects {
				if e.Operation == "pod.patch" && e.BeforePod.Spec.NodeName != "" {
					return fmt.Errorf("patched bound Pod %s", e.BeforePod.Name)
				}
			}
			return nil
		}},
		{Name: "bound-submission", Statement: "no submission for an identity with a bound member", Check: func(s registrationState) error {
			for _, e := range s.Effects {
				if e.Operation != "slurm.submit" || len(e.BoundPods) == 0 {
					continue
				}
				for _, job := range e.Submitted {
					key, err := worldAllocationKey(job, s.Allocations)
					if err != nil {
						return err
					}
					for _, allocation := range s.Allocations {
						if allocation.Key != key {
							continue
						}
						for _, component := range allocation.Components {
							for _, member := range component {
								if slices.Contains(e.BoundPods, member) {
									return fmt.Errorf("submitted %d for identity %s with bound Pod %s", e.JobID, key, member)
								}
							}
						}
					}
				}
			}
			return nil
		}},
		{Name: "duplicate-cancellation", Statement: "controller cancellation leaves another unfinished allocation for that identity", Check: func(s registrationState) error {
			for _, e := range s.Effects {
				if e.Operation != "slurm.cancel" {
					continue
				}
				var target api.V0044JobInfo
				for _, job := range e.BeforeJobs {
					if *job.JobId == e.JobID {
						target = job
					}
				}
				key, err := worldAllocationKey(target, s.Allocations)
				if err != nil {
					return err
				}
				other := false
				for _, job := range e.BeforeJobs {
					candidate, err := worldAllocationKey(job, s.Allocations)
					if err != nil {
						return err
					}
					if worldLeader(job) != worldLeader(target) && candidate == key && worldUnfinished(job) {
						other = true
					}
				}
				if !other {
					return fmt.Errorf("canceled job %d without an unfinished survivor for %s", e.JobID, key)
				}
			}
			return nil
		}},
		{Name: "survivor-preference", Statement: "a cancellation never removes the preferred allocation for its identity", Check: checkSurvivorPreference},
		{Name: "indexed-submission", Statement: "no submission observes an unfinished allocation in its indexed cache", Check: func(s registrationState) error {
			for _, e := range s.Effects {
				if e.Operation != "slurm.submit" {
					continue
				}
				for _, submitted := range e.Submitted {
					key, err := worldAllocationKey(submitted, s.Allocations)
					if err != nil {
						return err
					}
					for _, observed := range e.ObservedJobs {
						candidate, err := worldAllocationKey(observed, s.Allocations)
						if err != nil {
							return err
						}
						if key == candidate && worldUnfinished(observed) {
							return fmt.Errorf("submitted %d while cache contained unfinished %d", e.JobID, *observed.JobId)
						}
					}
				}
			}
			return nil
		}},
	},
	Eventually: []controllercheck.Rule[registrationState]{
		{Name: "registered", Statement: "each complete quorum without a canceled bound member has one unfinished allocation with correct labels and no registration gate", Check: checkRegistered},
	},
}

func checkSurvivorPreference(s registrationState) error {
	for _, e := range s.Effects {
		if e.Operation != "slurm.cancel" {
			continue
		}
		target, err := worldJobByID(e.BeforeJobs, e.JobID)
		if err != nil {
			return err
		}
		key, err := worldAllocationKey(target, s.Allocations)
		if err != nil {
			return err
		}
		best, bestLeader := -1, int32(0)
		for _, job := range e.BeforeJobs {
			candidate, err := worldAllocationKey(job, s.Allocations)
			if err != nil {
				return err
			}
			if candidate != key || !worldUnfinished(job) {
				continue
			}
			rank, leader := worldRank(job), worldLeader(job)
			if rank > best || (rank == best && leader < bestLeader) {
				best, bestLeader = rank, leader
			}
		}
		if bestLeader == worldLeader(target) {
			return fmt.Errorf("canceled preferred allocation %d (rank %d) for %s", e.JobID, best, key)
		}
	}
	return nil
}

func worldJobByID(jobs []api.V0044JobInfo, id int32) (api.V0044JobInfo, error) {
	for _, job := range jobs {
		if job.JobId != nil && *job.JobId == id {
			return job, nil
		}
	}
	return api.V0044JobInfo{}, fmt.Errorf("canceled job %d missing from pre-cancellation records", id)
}

// Rank only recorded state and priority, independently of controller helpers.
// Comparing every component makes an allocation inherit its best member's rank.
func worldRank(job api.V0044JobInfo) int {
	if job.JobState == nil {
		return 0
	}
	for _, state := range *job.JobState {
		if state == api.V0044JobInfoJobStateRUNNING || state == api.V0044JobInfoJobStateSUSPENDED || state == api.V0044JobInfoJobStateCOMPLETING {
			return 2
		}
	}
	if slices.Contains(*job.JobState, api.V0044JobInfoJobStatePENDING) && job.Priority != nil && job.Priority.Number != nil && *job.Priority.Number != 0 {
		return 1
	}
	return 0
}

func TestSurvivorPreferenceOracle(t *testing.T) {
	job := func(id, leader int32, state api.V0044JobInfoJobState, priority int32) api.V0044JobInfo {
		info := externaljobinfo.ExternalJobInfo{Pods: []string{"world/pod/uid"}}
		return api.V0044JobInfo{JobId: ptr.To(id), AdminComment: ptr.To(info.ToString()),
			JobState: &[]api.V0044JobInfoJobState{state},
			Priority: &api.V0044Uint32NoValStruct{Number: ptr.To(priority)},
			HetJobId: &api.V0044Uint32NoValStruct{Number: ptr.To(leader)}}
	}
	for _, tc := range []struct {
		name string
		jobs []api.V0044JobInfo
		keep int32
	}{
		{"started beats released", []api.V0044JobInfo{job(100, 0, api.V0044JobInfoJobStatePENDING, 100), job(200, 0, api.V0044JobInfoJobStateRUNNING, 0)}, 200},
		{"suspended beats released", []api.V0044JobInfo{job(100, 0, api.V0044JobInfoJobStatePENDING, 100), job(200, 0, api.V0044JobInfoJobStateSUSPENDED, 0)}, 200},
		{"completing beats released", []api.V0044JobInfo{job(100, 0, api.V0044JobInfoJobStatePENDING, 100), job(200, 0, api.V0044JobInfoJobStateCOMPLETING, 0)}, 200},
		{"released beats held", []api.V0044JobInfo{job(100, 0, api.V0044JobInfoJobStatePENDING, 0), job(200, 0, api.V0044JobInfoJobStatePENDING, 100)}, 200},
		{"lowest leader wins tie", []api.V0044JobInfo{job(200, 0, api.V0044JobInfoJobStatePENDING, 0), job(100, 0, api.V0044JobInfoJobStatePENDING, 0)}, 100},
		{"best component determines rank", []api.V0044JobInfo{job(100, 100, api.V0044JobInfoJobStatePENDING, 0), job(101, 100, api.V0044JobInfoJobStateRUNNING, 0), job(200, 0, api.V0044JobInfoJobStatePENDING, 100)}, 100},
		{"finished allocation excluded", []api.V0044JobInfo{job(100, 0, api.V0044JobInfoJobStateCANCELLED, 100), job(200, 0, api.V0044JobInfoJobStatePENDING, 0), job(300, 0, api.V0044JobInfoJobStatePENDING, 0)}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, target := range tc.jobs {
				if !worldUnfinished(target) {
					continue
				}
				s := registrationState{Effects: []registrationEffect{{Operation: "slurm.cancel", JobID: *target.JobId, BeforeJobs: tc.jobs}}}
				err := checkSurvivorPreference(s)
				if wantFailure := worldLeader(target) == tc.keep; (err != nil) != wantFailure {
					t.Fatalf("cancel %d: error=%v, want failure=%t", *target.JobId, err, wantFailure)
				}
			}
		})
	}
}

func worldIdentity(job api.V0044JobInfo) (externaljobinfo.ExternalJobInfo, error) {
	var info externaljobinfo.ExternalJobInfo
	err := externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, &info)
	return info, err
}

func worldAllocationKey(job api.V0044JobInfo, allocations []worldAllocation) (string, error) {
	info, err := worldIdentity(job)
	if err != nil {
		return "", err
	}
	if info.CompositePodGroup != nil {
		return info.CompositePodGroup.Key(), nil
	}
	if info.PodGroup != nil {
		return info.PodGroup.Key(), nil
	}
	if len(info.Pods) == 0 {
		return "", fmt.Errorf("job %v has no Pod identity", job.JobId)
	}
	// Legacy gang records have no group metadata. Resolve their full Pod UID
	// set against independently generated intent, not the controller's indexes.
	key := worldPodSet(info.Pods)
	for _, allocation := range allocations {
		if len(allocation.Components) == 1 && worldPodSet(allocation.Components[0]) == key {
			return allocation.Key, nil
		}
	}
	return key, nil
}

// Allocation identity is distinct from a component's job ID.
func worldLeader(job api.V0044JobInfo) int32 {
	if job.HetJobId != nil && job.HetJobId.Number != nil && *job.HetJobId.Number != 0 {
		return *job.HetJobId.Number
	}
	return *job.JobId
}

func checkRegistered(s registrationState) error {
	pods := make(map[string]corev1.Pod, len(s.Pods))
	for _, pod := range s.Pods {
		if pod.Spec.SchedulerName != "slurm-bridge" || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		pods[externaljobinfo.PodKey(&pod)] = pod
	}
	for _, allocation := range s.Allocations {
		complete, boundCanceled := true, false
		for _, component := range allocation.Components {
			for _, key := range component {
				pod, ok := pods[key]
				if !ok {
					complete = false
				}
				if pod.Spec.NodeName != "" {
					for _, job := range s.Jobs {
						if pod.Labels[wellknown.LabelExternalJobId] == strconv.FormatInt(int64(*job.JobId), 10) && !worldUnfinished(job) {
							boundCanceled = true
						}
					}
				}
			}
		}
		// All generated gangs require every original member. Losing quorum cannot
		// demand a new submission. Nor can a canceled bound member be resubmitted
		// before another controller cleans it up. Safety still applies in both cases.
		if !complete || boundCanceled {
			continue
		}
		leaders := map[int32]bool{}
		var active []api.V0044JobInfo
		for _, job := range s.Jobs {
			key, err := worldAllocationKey(job, s.Allocations)
			if err != nil {
				return err
			}
			if key == allocation.Key && worldUnfinished(job) {
				leaders[worldLeader(job)] = true
				active = append(active, job)
			}
		}
		if len(leaders) != 1 || len(active) != len(allocation.Components) {
			return fmt.Errorf("identity %s has leaders %v and %d components, want one allocation with %d components", allocation.Key, leaders, len(active), len(allocation.Components))
		}
		for _, component := range allocation.Components {
			var matched *api.V0044JobInfo
			for i := range active {
				info, err := worldIdentity(active[i])
				if err != nil {
					return err
				}
				if worldPodSet(info.Pods) == worldPodSet(component) {
					matched = &active[i]
				}
			}
			if matched == nil {
				return fmt.Errorf("identity %s has no component for %v", allocation.Key, component)
			}
			for _, key := range component {
				pod := pods[key]
				if hasRegistrationGate(&pod) || pod.Labels[wellknown.LabelExternalJobId] != strconv.FormatInt(int64(*matched.JobId), 10) {
					return fmt.Errorf("Pod %s has gate=%t label=%s, want job %d", pod.Name, hasRegistrationGate(&pod), pod.Labels[wellknown.LabelExternalJobId], *matched.JobId)
				}
				if len(allocation.Components) > 1 && (pod.Labels[wellknown.LabelExternalHetJobId] != strconv.FormatInt(int64(worldLeader(*matched)), 10) || pod.Labels[wellknown.LabelExternalHetJobOffset] != strconv.FormatInt(int64(*matched.HetJobOffset.Number), 10)) {
					return fmt.Errorf("Pod %s has wrong heterogeneous allocation labels: %v", pod.Name, pod.Labels)
				}
			}
		}
	}
	return nil
}

// This oracle uses raw states, independently of controller conversion helpers.
func worldUnfinished(job api.V0044JobInfo) bool {
	return worldRank(job) == 2 || (job.JobState != nil && slices.Contains(*job.JobState, api.V0044JobInfoJobStatePENDING))
}

func TestRegistrationContract(t *testing.T) {
	completed := map[string]int{}
	t.Cleanup(func() { t.Logf("completed generated actions: %v", completed) })
	controllercheck.Explore(t, registrationContract, func(rt *rapid.T) controllercheck.Campaign[registrationState] {
		w, err := newRegistrationWorld(rt.Context(), rapid.IntRange(1, 4).Draw(rt, "slots"))
		if err != nil {
			rt.Fatal(err)
		}
		campaign := w.campaign()
		// Sample reachable starting phases so late lifecycle actions are exercised
		// during disturbances, not only after recovery has stopped those actions.
		phase := rapid.SampledFrom([]string{"empty", "registered", "running"}).Draw(rt, "initial-phase")
		if phase != "empty" {
			if err := w.create(rt); err != nil {
				rt.Fatal(err)
			}
			// Reach the chosen phase through real reconciler passes, using the
			// same convergence bound as recovery rather than a fixed tick count.
			var pending error
			for range campaign.RecoverySteps {
				if err := campaign.Progress(rt.Context()); err != nil {
					rt.Fatal(err)
				}
				state, err := w.snapshot(rt.Context())
				if err != nil {
					rt.Fatal(err)
				}
				pending = checkRegistered(state)
				if pending == nil {
					break
				}
			}
			if pending != nil {
				rt.Fatalf("initial registration did not settle: %v", pending)
			}
			if phase == "running" {
				jobs, err := worldJobs(rt.Context(), w.live)
				if err != nil {
					rt.Fatal(err)
				}
				for _, job := range jobs {
					if *job.JobId == worldLeader(job) {
						if err := w.transitionAllocation(rt.Context(), *job.JobId, api.V0044JobInfoJobStateRUNNING); err != nil {
							rt.Fatal(err)
						}
					}
				}
			}
		}
		for name, action := range campaign.Actions {
			campaign.Actions[name] = func(rt *rapid.T) error {
				err := action(rt)
				if err == nil {
					completed[name]++
				}
				return err
			}
		}
		return campaign
	})
}
