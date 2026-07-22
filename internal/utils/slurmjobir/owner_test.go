// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func controllerOwner(apiVersion, kind, name string) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       name,
		Controller: ptr.To(true),
	}
}

func TestGetRootOwnerMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))

	type testCase struct {
		name    string
		client  client.Client
		pod     *corev1.Pod
		want    *metav1.PartialObjectMetadata
		wantErr bool
	}
	basePod := st.MakePod().Name("pod1").Obj()
	tests := []testCase{
		{
			name:   "Pod",
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(basePod.DeepCopy()).Build(),
			pod:    basePod.DeepCopy(),
			want: &metav1.PartialObjectMetadata{
				TypeMeta:   pod_v1,
				ObjectMeta: metav1.ObjectMeta{Name: basePod.Name},
			},
		},
		func() testCase {
			rs := &appsv1.ReplicaSet{
				TypeMeta:   metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "ReplicaSet"},
				ObjectMeta: metav1.ObjectMeta{Name: "replicaset1"},
			}
			pod := basePod.DeepCopy()
			pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(rs.APIVersion, rs.Kind, rs.Name)}
			return testCase{
				name:   "ReplicaSet => Pod",
				client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(rs, pod).Build(),
				pod:    pod,
				want: &metav1.PartialObjectMetadata{
					TypeMeta:   rs.TypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: rs.Name},
				},
			}
		}(),
		func() testCase {
			deployment := &appsv1.Deployment{
				TypeMeta:   metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"},
				ObjectMeta: metav1.ObjectMeta{Name: "deployment1"},
			}
			rs := &appsv1.ReplicaSet{
				TypeMeta: metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "ReplicaSet"},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "replicaset1",
					OwnerReferences: []metav1.OwnerReference{controllerOwner(deployment.APIVersion, deployment.Kind, deployment.Name)},
				},
			}
			pod := basePod.DeepCopy()
			pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(rs.APIVersion, rs.Kind, rs.Name)}
			return testCase{
				name:   "Deployment => ReplicaSet => Pod",
				client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, rs, pod).Build(),
				pod:    pod,
				want: &metav1.PartialObjectMetadata{
					TypeMeta:   deployment.TypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: deployment.Name},
				},
			}
		}(),
		func() testCase {
			rs := &appsv1.ReplicaSet{
				TypeMeta: metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "ReplicaSet"},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "replicaset1",
					OwnerReferences: []metav1.OwnerReference{controllerOwner(appsv1.SchemeGroupVersion.String(), "Deployment", "missing-deployment")},
				},
			}
			pod := basePod.DeepCopy()
			pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(rs.APIVersion, rs.Kind, rs.Name)}
			return testCase{
				name:    "missing higher owner",
				client:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(rs, pod).Build(),
				pod:     pod,
				wantErr: true,
			}
		}(),
		func() testCase {
			pod := basePod.DeepCopy()
			pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(appsv1.SchemeGroupVersion.String(), "ReplicaSet", "missing-replicaset")}
			return testCase{
				name:    "missing direct owner",
				client:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
				pod:     pod,
				wantErr: true,
			}
		}(),
		func() testCase {
			job := &batchv1.Job{
				TypeMeta: metav1.TypeMeta{APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job"},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "job1",
					OwnerReferences: []metav1.OwnerReference{controllerOwner(batchv1.SchemeGroupVersion.String(), "Job", "job1")},
				},
			}
			pod := basePod.DeepCopy()
			pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(job.APIVersion, job.Kind, job.Name)}
			return testCase{
				name:    "controller owner cycle exceeds maximum depth",
				client:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, pod).Build(),
				pod:     pod,
				wantErr: true,
			}
		}(),
		func() testCase {
			job := &batchv1.Job{
				TypeMeta:   metav1.TypeMeta{APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job"},
				ObjectMeta: metav1.ObjectMeta{Name: "job1"},
			}
			pod := basePod.DeepCopy()
			pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(job.APIVersion, job.Kind, job.Name)}
			return testCase{
				name:   "Job => Pod",
				client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, pod).Build(),
				pod:    pod,
				want: &metav1.PartialObjectMetadata{
					TypeMeta:   job.TypeMeta,
					ObjectMeta: metav1.ObjectMeta{Name: job.Name},
				},
			}
		}(),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getRootOwnerMetadata(tt.client, context.TODO(), tt.pod)
			if (err != nil) != tt.wantErr {
				t.Fatalf("getRootOwnerMetadata() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("getRootOwnerMetadata() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetRootOwnerMetadataFallbackPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))

	deploymentGVK := appsv1.SchemeGroupVersion.WithKind("Deployment")
	jobSetGVK := schema.FromAPIVersionAndKind("jobset.x-k8s.io/v1alpha2", "JobSet")
	missingGVK := schema.FromAPIVersionAndKind("example.com/v1", "MissingController")
	tests := []struct {
		name                string
		parentGVK           schema.GroupVersionKind
		getErr              error
		wantResolvedJobRoot bool
	}{
		{
			name:                "forbidden unsupported parent falls back",
			parentGVK:           deploymentGVK,
			getErr:              apierrors.NewForbidden(deploymentGVK.GroupVersion().WithResource("deployments").GroupResource(), "deployment1", errors.New("access denied")),
			wantResolvedJobRoot: true,
		},
		{
			name:      "forbidden supported parent returns error",
			parentGVK: jobSetGVK,
			getErr:    apierrors.NewForbidden(jobSetGVK.GroupVersion().WithResource("jobsets").GroupResource(), "jobset1", errors.New("access denied")),
		},
		{
			name:      "missing parent returns error",
			parentGVK: missingGVK,
			getErr:    apierrors.NewNotFound(missingGVK.GroupVersion().WithResource("missingcontrollers").GroupResource(), "missing1"),
		},
		{
			name:      "unserved parent kind returns error",
			parentGVK: missingGVK,
			getErr: &apimeta.NoKindMatchError{
				GroupKind:        missingGVK.GroupKind(),
				SearchedVersions: []string{missingGVK.Version},
			},
		},
		{
			name:      "transient parent error returns error",
			parentGVK: deploymentGVK,
			getErr:    apierrors.NewInternalError(errors.New("temporary API failure")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const parentName = "parent1"
			job := &batchv1.Job{
				TypeMeta: metav1.TypeMeta{APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job"},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "job1",
					OwnerReferences: []metav1.OwnerReference{controllerOwner(tt.parentGVK.GroupVersion().String(), tt.parentGVK.Kind, parentName)},
				},
			}
			pod := st.MakePod().Name("pod1").Obj()
			pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(job.APIVersion, job.Kind, job.Name)}
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(job, pod).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if key.Name == parentName {
							return tt.getErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).
				Build()

			got, err := getRootOwnerMetadata(cl, context.TODO(), pod)
			if tt.wantResolvedJobRoot {
				if err != nil {
					t.Fatalf("getRootOwnerMetadata() error = %v", err)
				}
				if got.TypeMeta != job_v1 || got.Name != job.Name {
					t.Errorf("getRootOwnerMetadata() = %v %q, want Job %q", got.TypeMeta, got.Name, job.Name)
				}
				return
			}
			if err == nil {
				t.Fatalf("getRootOwnerMetadata() error = nil, want %v", tt.getErr)
			}
		})
	}
}
