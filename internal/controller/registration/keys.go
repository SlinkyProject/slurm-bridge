// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

// Request is a comparable allocation identity, including its incarnation UID.
// ReplicaHash distinguishes the independent allocations of a legacy LWS.
type Request struct {
	APIGroup, Kind, Namespace, Name string
	UID                             types.UID
	ReplicaHash                     string
}

func (r Request) String() string {
	switch r.Kind {
	case "Pod":
		return "pod/" + r.podKey()
	case "LeaderWorkerSet":
		return "lws/" + r.Namespace + "/" + r.Name + "/" + string(r.UID) + "/" + r.ReplicaHash
	default:
		return r.groupIdentity().Key()
	}
}

func (r Request) objectKey() client.ObjectKey {
	return client.ObjectKey{Namespace: r.Namespace, Name: r.Name}
}

func (r Request) podKey() string {
	return externaljobinfo.PodKey(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: r.Namespace, Name: r.Name, UID: r.UID}})
}

func (r Request) groupIdentity() externaljobinfo.GroupIdentity {
	return externaljobinfo.GroupIdentity{APIGroup: r.APIGroup, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, UID: r.UID}
}

func groupKey(group externaljobinfo.GroupIdentity) Request {
	return Request{APIGroup: group.APIGroup, Kind: group.Kind, Namespace: group.Namespace, Name: group.Name, UID: group.UID}
}

func objectRequest(obj client.Object) Request {
	gvk := obj.GetObjectKind().GroupVersionKind()
	return Request{APIGroup: gvk.Group, Kind: gvk.Kind, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: obj.GetUID()}
}

func allocationKey(pod *corev1.Pod, root metav1.PartialObjectMetadata) Request {
	if group := externaljobinfo.NewGroupIdentity(root); group != nil {
		return groupKey(*group)
	}
	if root.Kind == "LeaderWorkerSet" {
		key := objectRequest(&root)
		key.ReplicaHash = pod.Labels[lwsv1.GroupUniqueHashLabelKey]
		return key
	}
	return podRequest(pod)
}

func podRequest(pod *corev1.Pod) Request {
	return Request{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}
}

func (r *Reconciler) typeMeta(key Request) (metav1.TypeMeta, error) {
	switch {
	case key.APIGroup == lwsv1.GroupVersion.Group && key.Kind == "LeaderWorkerSet":
		return metav1.TypeMeta{APIVersion: lwsv1.GroupVersion.String(), Kind: key.Kind}, nil
	case key.APIGroup == "scheduling.k8s.io" && r.workloadAPI != nil:
		switch key.Kind {
		case "PodGroup":
			return r.workloadAPI.PodGroupTypeMeta, nil
		case "CompositePodGroup":
			return metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha3", Kind: key.Kind}, nil
		}
	case key.APIGroup == "scheduling.x-k8s.io" && key.Kind == "PodGroup":
		return metav1.TypeMeta{APIVersion: "scheduling.x-k8s.io/v1alpha1", Kind: key.Kind}, nil
	}
	return metav1.TypeMeta{}, fmt.Errorf("unsupported allocation key %q", key)
}

// members also returns the group/Pod index identity for cleanup when the owning
// Kubernetes object no longer exists. Legacy LWS lookup needs its current leader.
// Grouped Pod requests redirect before any Slurm lookup or duplicate cleanup.
func (r *Reconciler) members(ctx context.Context, key Request) ([]corev1.Pod, slurmjob.AllocationIdentity, error) {
	identity := slurmjob.AllocationIdentity{Index: slurmjob.GroupJobIndex, Key: key.groupIdentity().Key()}
	if key.Kind == "Pod" {
		identity.Index, identity.Key = slurmjob.PodJobIndex, key.podKey()
		pod := &corev1.Pod{}
		err := r.Get(ctx, key.objectKey(), pod)
		if err != nil {
			return nil, identity, client.IgnoreNotFound(err)
		}
		if key.UID != "" && key.UID != pod.UID {
			return nil, identity, nil
		}
		if pod.Spec.SchedulerName == r.schedulerName {
			root, err := slurmjobir.WorkloadRootForPod(r.Client, r.workloadAPI, ctx, pod)
			if err != nil {
				return nil, slurmjob.AllocationIdentity{}, err
			}
			if current := allocationKey(pod, *root); current != key {
				r.enqueue(current)
				return nil, slurmjob.AllocationIdentity{}, nil
			}
		}
		return []corev1.Pod{*pod}, identity, nil
	}
	if key.APIGroup == "scheduling.k8s.io" && r.workloadAPI == nil {
		return nil, identity, nil
	}
	typeMeta, err := r.typeMeta(key)
	if err != nil {
		return nil, identity, err
	}
	root := &metav1.PartialObjectMetadata{TypeMeta: typeMeta, ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: key.UID}}
	if key.Kind == "LeaderWorkerSet" {
		identity.Index = "" // Without a current leader there is no Pod index key.
	}
	uid := root.UID
	if err := r.Get(ctx, client.ObjectKeyFromObject(root), root); err != nil {
		return nil, identity, client.IgnoreNotFound(err)
	}
	if root.UID != uid {
		current := key
		current.UID = root.UID
		r.enqueue(current)
		return nil, identity, nil
	}
	pods, err := slurmjobir.RegistrationMembers(r.Client, r.workloadAPI, ctx, root, key.ReplicaHash)
	if err != nil {
		return nil, identity, err
	}
	if len(pods) > 0 {
		// For legacy groups the current members supply the pre-upgrade lookup keys.
		ir := &slurmjobir.SlurmJobIR{RootPOM: *root, Components: []slurmjobir.SlurmJobComponent{{Pods: corev1.PodList{Items: pods}}}}
		identity, err = slurmjob.IdentityForAllocation(&pods[0], ir)
	}
	return pods, identity, err
}
