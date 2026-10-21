// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Immutable workload intent, generated independently of translation/submission.
// Every generated gang requires all its original members. Deletion can remove
// quorum; the world does not run workload controllers to replace missing Pods.
type worldAllocation struct {
	Key        string
	Components [][]string
}

func worldPodSet(keys []string) string {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

func (w *registrationWorld) create(rt *rapid.T) error {
	state, err := w.snapshot(w.ctx)
	if err != nil {
		return err
	}
	var slots []string
	for i := range w.slots {
		slot := strconv.Itoa(i)
		if !slices.ContainsFunc(state.Pods, func(p corev1.Pod) bool { return p.Labels["property-test/slot"] == slot }) {
			slots = append(slots, slot)
		}
	}
	if len(slots) == 0 {
		rt.Skip("all workload slots occupied")
	}
	slot := rapid.SampledFrom(slots).Draw(rt, "slot")
	kind := rapid.SampledFrom([]string{"Pod", "Job", "PodGroup", "CompositePodGroup", "LeaderWorkerSet"}).Draw(rt, "workload-kind")
	w.incarnation++
	rootName := fmt.Sprintf("workload-%s-%d", slot, w.incarnation)
	meta := metav1.ObjectMeta{Namespace: "world", Name: rootName, UID: types.UID(rootName)}
	size, components, replicas := 1, 1, 1
	if kind == "PodGroup" || kind == "CompositePodGroup" || kind == "LeaderWorkerSet" {
		size = rapid.IntRange(2, 3).Draw(rt, "members")
	}
	if kind == "CompositePodGroup" {
		components = rapid.IntRange(2, 3).Draw(rt, "components")
	}
	if kind == "LeaderWorkerSet" {
		replicas = rapid.IntRange(2, 3).Draw(rt, "replicas")
	}
	spec := corev1.PodSpec{SchedulerName: "slurm-bridge", Containers: []corev1.Container{{Name: "worker", Image: "busybox"}},
		SchedulingGates: []corev1.PodSchedulingGate{{Name: wellknown.SchedulingGateRegistration}}}
	if rapid.Bool().Draw(rt, "unrelated-gate") {
		spec.SchedulingGates = append(spec.SchedulingGates, corev1.PodSchedulingGate{Name: "other/gate"})
	}
	var owner *metav1.OwnerReference
	var root client.Object
	switch kind {
	case "Job":
		root = &batchv1.Job{ObjectMeta: meta, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: spec}}}
		owner = &metav1.OwnerReference{APIVersion: "batch/v1", Kind: kind, Name: rootName, UID: meta.UID, Controller: ptr.To(true)}
	case "LeaderWorkerSet":
		root = &lwsv1.LeaderWorkerSet{ObjectMeta: meta, Spec: lwsv1.LeaderWorkerSetSpec{Replicas: ptr.To(int32(replicas)),
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{Size: ptr.To(int32(size)), WorkerTemplate: corev1.PodTemplateSpec{Spec: spec}}}}
		owner = &metav1.OwnerReference{APIVersion: lwsv1.GroupVersion.String(), Kind: kind, Name: rootName, UID: meta.UID, Controller: ptr.To(true)}
	case "CompositePodGroup":
		root = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "scheduling.k8s.io/v1alpha3", "kind": kind,
			"metadata": map[string]any{"namespace": meta.Namespace, "name": meta.Name, "uid": string(meta.UID)},
			"spec":     map[string]any{"schedulingPolicy": map[string]any{"gang": map[string]any{"minGroupCount": int64(components)}}},
		}}
	}
	if root != nil {
		if err := w.kube.Create(w.ctx, root); err != nil {
			return err
		}
	}
	for replica := range replicas {
		allocation := worldAllocation{}
		if kind == "PodGroup" || kind == "CompositePodGroup" {
			allocation.Key = (externaljobinfo.GroupIdentity{APIGroup: "scheduling.k8s.io", Kind: kind, Namespace: meta.Namespace, Name: rootName, UID: meta.UID}).Key()
		}
		for component := range components {
			groupName := rootName
			if kind == "CompositePodGroup" {
				groupName = fmt.Sprintf("%s-component-%d", rootName, component)
			}
			if kind == "PodGroup" || kind == "CompositePodGroup" {
				group := &slurmjobir.PodGroup{TypeMeta: w.workloadAPI.PodGroupTypeMeta,
					ObjectMeta: metav1.ObjectMeta{Namespace: meta.Namespace, Name: groupName, UID: types.UID(groupName)},
					Spec:       slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: int32(size)}}}}
				if kind == "CompositePodGroup" {
					group.Spec.ParentCompositePodGroupName = ptr.To(rootName)
				}
				if err := w.kube.Create(w.ctx, group); err != nil {
					return err
				}
			}
			var members []string
			for member := range size {
				name := fmt.Sprintf("pod-%s-%d-%d-%d", slot, replica, component, member)
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: meta.Namespace, Name: name,
					UID: types.UID(fmt.Sprintf("%s-%d", name, w.incarnation)), Labels: map[string]string{"property-test/slot": slot}}, Spec: *spec.DeepCopy()}
				if owner != nil {
					pod.OwnerReferences = []metav1.OwnerReference{*owner}
				}
				if kind == "PodGroup" || kind == "CompositePodGroup" {
					pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(groupName)}
				}
				if kind == "LeaderWorkerSet" {
					pod.Labels[lwsv1.GroupUniqueHashLabelKey] = fmt.Sprintf("%s-replica-%d", rootName, replica)
					pod.Labels[lwsv1.GroupIndexLabelKey] = strconv.Itoa(replica)
					pod.Labels[lwsv1.WorkerIndexLabelKey] = strconv.Itoa(member)
					pod.Labels[lwsv1.SetNameLabelKey] = rootName
				}
				if err := w.kube.Create(w.ctx, pod); err != nil {
					return err
				}
				members = append(members, externaljobinfo.PodKey(pod))
				w.queue.Add(podRequest(pod))
			}
			allocation.Components = append(allocation.Components, members)
		}
		if allocation.Key == "" {
			allocation.Key = worldPodSet(allocation.Components[0])
		}
		w.allocations = append(w.allocations, allocation)
	}
	w.trace.Logf("create: %s %s size=%d components=%d replicas=%d", kind, rootName, size, components, replicas)
	return nil
}
