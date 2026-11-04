// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const podGroupMemberIndex = "bridge.lifecycle-podgroup"
const publishedJobIndex = "bridge.lifecycle-job"

func nativePodGroupKeys(obj client.Object) []string {
	pod := obj.(*corev1.Pod)
	if pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
		return nil
	}
	return []string{*pod.Spec.SchedulingGroup.PodGroupName}
}

func publishedJobKeys(obj client.Object) []string {
	if id := obj.GetLabels()[wellknown.LabelExternalJobId]; id != "" {
		return []string{id}
	}
	return nil
}

// indexedMembershipClient serves translation's namespace-wide Pod reads from field indexes, so
// a reconcile does not copy every Pod in the namespace.
type indexedMembershipClient struct {
	client.Client
}

func (r indexedMembershipClient) ListPodGroupMembers(ctx context.Context, namespace, name string) (corev1.PodList, error) {
	pods := corev1.PodList{}
	err := r.Client.List(ctx, &pods, client.InNamespace(namespace), client.MatchingFields{podGroupMemberIndex: name})
	return pods, err
}

func (r indexedMembershipClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	options := (&client.ListOptions{}).ApplyOptions(opts)
	if _, ok := list.(*corev1.PodList); ok && options.LabelSelector != nil {
		if id, exact := options.LabelSelector.RequiresExactMatch(wellknown.LabelExternalJobId); exact {
			opts = append(opts, client.MatchingFields{publishedJobIndex: id})
		}
	}
	return r.Client.List(ctx, list, opts...)
}

// liveMembershipReader keeps lifecycle reads live except PodGroup membership, which
// comes from the cache index instead of a live List of every Pod in the namespace.
// componentMembers re-reads every Pod published to the job live, so the cache only
// supplies unpublished members, which cannot hold the allocation.
type liveMembershipReader struct {
	client.Reader
	members indexedMembershipClient
}

func (r liveMembershipReader) ListPodGroupMembers(ctx context.Context, namespace, name string) (corev1.PodList, error) {
	return r.members.ListPodGroupMembers(ctx, namespace, name)
}

// liveMembers returns the reader for release decisions.
func (r *Reconciler) liveMembers() client.Reader {
	if r.membersIndexed {
		return liveMembershipReader{Reader: r.apiReader, members: indexedMembershipClient{Client: r.Client}}
	}
	return r.apiReader
}

// cachedMembers returns the client for translation and membership reads.
func (r *Reconciler) cachedMembers() client.Client {
	if r.membersIndexed {
		return indexedMembershipClient{Client: r.Client}
	}
	return r.Client
}
