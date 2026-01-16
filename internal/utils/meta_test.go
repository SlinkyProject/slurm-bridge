// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

func TestGetRootOwnerMetadata(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(kubescheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	utilruntime.Must(sched.AddToScheme(scheme))
	pod := st.MakePod().Name("pod1").Obj()
	type args struct {
		c   client.Client
		ctx context.Context
		obj client.Object
	}
	tests := []struct {
		name    string
		args    args
		want    *metav1.PartialObjectMetadata
		wantErr bool
	}{
		{
			name: "Pod",
			args: args{
				c:   fake.NewFakeClient(pod.DeepCopy()),
				ctx: context.TODO(),
				obj: pod,
			},
			want: &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Pod",
					APIVersion: "v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1",
				},
			},
			wantErr: false,
		},
		{
			name: "ReplicaSet => Pod",
			args: func() args {
				replicaSet := &appsv1.ReplicaSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ReplicaSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "replicaset1",
					},
				}
				pod := pod.DeepCopy()
				pod.OwnerReferences = []metav1.OwnerReference{
					{
						Name:       replicaSet.GetName(),
						Kind:       replicaSet.Kind,
						APIVersion: replicaSet.APIVersion,
						Controller: ptr.To(true),
					},
				}
				return args{
					c:   fake.NewFakeClient(replicaSet, pod),
					ctx: context.TODO(),
					obj: pod,
				}
			}(),
			want: &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ReplicaSet",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "replicaset1",
				},
			},
			wantErr: false,
		},
		{
			name: "Deployment => ReplicaSet => Pod",
			args: func() args {
				deployment := &appsv1.Deployment{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Deployment",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "deployment1",
					},
				}
				replicaSet := &appsv1.ReplicaSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ReplicaSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "replicaset1",
					},
				}
				replicaSet.OwnerReferences = []metav1.OwnerReference{
					{
						Name:       deployment.GetName(),
						Kind:       deployment.Kind,
						APIVersion: deployment.APIVersion,
						Controller: ptr.To(true),
					},
				}
				pod := pod.DeepCopy()
				pod.OwnerReferences = []metav1.OwnerReference{
					{
						Name:       replicaSet.GetName(),
						Kind:       replicaSet.Kind,
						APIVersion: replicaSet.APIVersion,
						Controller: ptr.To(true),
					},
				}
				return args{
					c:   fake.NewFakeClient(deployment, replicaSet, pod),
					ctx: context.TODO(),
					obj: pod,
				}
			}(),
			want: &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "apps/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "deployment1",
				},
			},
			wantErr: false,
		},
		{
			name: "Job => Pod",
			args: func() args {
				job := &batchv1.Job{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Job",
						APIVersion: "batch/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "job1",
					},
				}
				pod := pod.DeepCopy()
				pod.OwnerReferences = []metav1.OwnerReference{
					{
						Name:       job.GetName(),
						Kind:       job.Kind,
						APIVersion: job.APIVersion,
						Controller: ptr.To(true),
					},
				}
				return args{
					c:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, pod).Build(),
					ctx: context.TODO(),
					obj: pod,
				}
			}(),
			want: &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Job",
					APIVersion: "batch/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "job1",
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetRootOwnerMetadata(tt.args.c, tt.args.ctx, tt.args.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetRootOwnerMetadata() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("GetRootOwnerMetadata() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNamespacedNameFromString(t *testing.T) {
	tests := []struct {
		name          string
		qualifiedName string
		want          types.NamespacedName
	}{
		{
			name:          "empty",
			qualifiedName: "",
			want:          types.NamespacedName{},
		},
		{
			name:          "qualified",
			qualifiedName: "foo/bar",
			want: types.NamespacedName{
				Namespace: "foo",
				Name:      "bar",
			},
		},
		{
			name:          "name",
			qualifiedName: "bar",
			want: types.NamespacedName{
				Name: "bar",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NamespacedNameFromString(tt.qualifiedName)
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("NamespacedNameFromString() = %v, want %v", got, tt.want)
			}
		})
	}
}
