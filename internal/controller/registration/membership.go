// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

var compositePodGroupKind = schema.GroupKind{Group: "scheduling.k8s.io", Kind: "CompositePodGroup"}

// indexedMembershipClient serves translation's namespace-wide reads from the cache. Without it,
// each reconcile listed every Pod in the namespace and fetched every CompositePodGroup from the
// apiserver, because the default client does not cache unstructured objects.
type indexedMembershipClient struct {
	client.Client
	cache client.Reader // nil leaves CompositePodGroups uncached
}

func (r indexedMembershipClient) ListPodGroupMembers(ctx context.Context, namespace, name string) (corev1.PodList, error) {
	pods := corev1.PodList{}
	err := r.Client.List(ctx, &pods, client.InNamespace(namespace), client.MatchingFields{podGroupMemberIndex: name})
	return pods, err
}

func (r indexedMembershipClient) compositePodGroup(obj runtime.Object) bool {
	var gvk schema.GroupVersionKind
	switch o := obj.(type) {
	case *unstructured.Unstructured:
		gvk = o.GroupVersionKind()
	case *unstructured.UnstructuredList:
		gvk = o.GroupVersionKind()
		gvk.Kind = strings.TrimSuffix(gvk.Kind, "List")
	default:
		return false
	}
	return r.cache != nil && gvk.GroupKind() == compositePodGroupKind
}

func (r indexedMembershipClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if r.compositePodGroup(obj) {
		return r.cache.Get(ctx, key, obj, opts...)
	}
	return r.Client.Get(ctx, key, obj, opts...)
}

func (r indexedMembershipClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if r.compositePodGroup(list) {
		// Translation only decodes CompositePodGroups, so it reads them in place instead of copying the namespace.
		return r.cache.List(ctx, list, append(opts, client.UnsafeDisableDeepCopy)...)
	}
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
		return indexedMembershipClient{Client: r.Client, cache: r.cache}
	}
	return r.Client
}
