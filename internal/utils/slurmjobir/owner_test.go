// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func controllerOwner(apiVersion, kind, name string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, Controller: ptr.To(true)}
}

func TestWorkloadOwnerPaths(t *testing.T) {
	statefulSet := metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}
	unknown := metav1.TypeMeta{APIVersion: "example.test/v1", Kind: "Controller"}
	tests := []struct {
		name   string
		owners []metav1.TypeMeta
		want   metav1.TypeMeta
		reads  int
	}{
		{name: "bare Pod", want: pod_v1},
		{name: "unknown direct owner", owners: []metav1.TypeMeta{unknown, job_v1}, want: pod_v1},
		{name: "unknown Job API group", owners: []metav1.TypeMeta{{APIVersion: "example.test/v1", Kind: "Job"}}, want: pod_v1},
		{name: "unsupported Job API version", owners: []metav1.TypeMeta{{APIVersion: "batch/v9", Kind: "Job"}}, want: pod_v1},
		{name: "Job", owners: []metav1.TypeMeta{job_v1}, want: job_v1, reads: 1},
		{name: "JobSet", owners: []metav1.TypeMeta{job_v1, jobSet_v1alpha2}, want: jobSet_v1alpha2, reads: 2},
		{name: "stop above Job", owners: []metav1.TypeMeta{job_v1, unknown, jobSet_v1alpha2}, want: job_v1, reads: 1},
		{name: "stop above JobSet", owners: []metav1.TypeMeta{job_v1, jobSet_v1alpha2, unknown}, want: jobSet_v1alpha2, reads: 2},
		{name: "Job cycle", owners: []metav1.TypeMeta{job_v1, job_v1}, want: job_v1, reads: 1},
		{name: "LWS direct", owners: []metav1.TypeMeta{lws_v1}, want: lws_v1, reads: 1},
		{name: "LWS ordinal leader", owners: []metav1.TypeMeta{statefulSet, lws_v1}, want: lws_v1, reads: 2},
		{name: "LWS ordinal worker", owners: []metav1.TypeMeta{statefulSet, pod_v1, statefulSet, lws_v1}, want: lws_v1, reads: 4},
		{name: "LWS hash worker", owners: []metav1.TypeMeta{pod_v1, lws_v1}, want: lws_v1, reads: 2},
		{name: "ordinary StatefulSet", owners: []metav1.TypeMeta{statefulSet, unknown}, want: pod_v1, reads: 1},
		{name: "unsupported LWS path", owners: []metav1.TypeMeta{statefulSet, job_v1, lws_v1}, want: pod_v1, reads: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default", UID: "pod-uid"}}
			var objects []client.Object
			var child client.Object = pod
			for i, kind := range tt.owners {
				name := string(rune('a' + i))
				child.SetOwnerReferences([]metav1.OwnerReference{controllerOwner(kind.APIVersion, kind.Kind, name)})
				parent := &metav1.PartialObjectMetadata{TypeMeta: kind, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pod.Namespace}}
				objects = append(objects, parent)
				child = parent
			}
			reads := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					reads++
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()
			got, err := getRootOwnerMetadata(c, t.Context(), pod)
			if err != nil {
				t.Fatal(err)
			}
			if got.TypeMeta != tt.want || reads != tt.reads {
				t.Fatalf("root=%v, reads=%d; want %v, %d", got.TypeMeta, reads, tt.want, tt.reads)
			}
			if tt.want == pod_v1 && (got.Name != pod.Name || got.UID != pod.UID) {
				t.Fatalf("fallback root=%s, want Pod", got.Name)
			}
		})
	}
}

func TestKnownOwnerLookupErrors(t *testing.T) {
	for _, cause := range []error{
		apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "job", errors.New("denied")),
		apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, "job"),
		apierrors.NewServiceUnavailable("unavailable"),
	} {
		scheme := runtime.NewScheme()
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", OwnerReferences: []metav1.OwnerReference{controllerOwner(job_v1.APIVersion, job_v1.Kind, "job")}}}
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return cause
			},
		}).Build()
		if _, err := getRootOwnerMetadata(c, t.Context(), pod); !errors.Is(err, cause) {
			t.Fatalf("error=%v, want %v", err, cause)
		}
	}
}
