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

type indexedMembershipReader struct{ client.Reader }

func (r indexedMembershipReader) ListPodGroupMembers(ctx context.Context, namespace, name string) (corev1.PodList, error) {
	pods := corev1.PodList{}
	err := r.Reader.List(ctx, &pods, client.InNamespace(namespace), client.MatchingFields{podGroupMemberIndex: name})
	return pods, err
}

func (r indexedMembershipReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	options := (&client.ListOptions{}).ApplyOptions(opts)
	if _, ok := list.(*corev1.PodList); ok && options.LabelSelector != nil {
		if id, exact := options.LabelSelector.RequiresExactMatch(wellknown.LabelExternalJobId); exact {
			opts = append(opts, client.MatchingFields{publishedJobIndex: id})
		}
	}
	return r.Reader.List(ctx, list, opts...)
}

func (r *Reconciler) cachedMembers() client.Reader {
	if r.membersIndexed {
		return indexedMembershipReader{Reader: r.Client}
	}
	return r.Client
}
