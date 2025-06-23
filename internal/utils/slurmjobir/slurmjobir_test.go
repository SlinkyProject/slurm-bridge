// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTranslateToSlurmJobIR(t *testing.T) {
	podWithAnnotation := st.MakePod().Namespace("default").Name("testpod").Annotations(map[string]string{wellknown.AnnotationAccount: "test1", wellknown.AnnotationGroupId: "1000", wellknown.AnnotationUserId: "1000"}).Obj()
	podWithBadAnnotation := st.MakePod().Namespace("default").Name("testpod").Annotations(map[string]string{wellknown.AnnotationCpuPerTask: "NaN"}).Obj()
	type args struct {
		client client.Client
		ctx    context.Context
		pod    *corev1.Pod
	}
	tests := []struct {
		name    string
		args    args
		want    *SlurmJobIR
		wantErr bool
	}{
		{
			name: "Empty pod",
			args: args{
				client: fake.NewFakeClient(),
				ctx:    context.TODO(),
				pod:    &corev1.Pod{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Pod with annotation",
			args: args{
				client: fake.NewFakeClient(podWithAnnotation.DeepCopy()),
				ctx:    context.TODO(),
				pod:    podWithAnnotation.DeepCopy(),
			},
			want: &SlurmJobIR{
				RootPOM: metav1.PartialObjectMetadata{
					TypeMeta: pod_v1,
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testpod",
						Namespace: "default",
						Annotations: map[string]string{
							wellknown.AnnotationAccount: "test1",
							wellknown.AnnotationGroupId: "1000",
							wellknown.AnnotationUserId:  "1000",
						},
						ResourceVersion: "999",
					},
				},
				Pods: corev1.PodList{
					Items: []corev1.Pod{*podWithAnnotation.DeepCopy()},
				},
				JobInfo: SlurmJobIRJobInfo{
					Account: ptr.To("test1"),
					GroupId: ptr.To("1000"),
					MaxNodes: func() *int32 {
						maxNodes := int32(1)
						return &maxNodes
					}(),
					TasksPerNode: func() *int32 {
						tasksPerNode := int32(1)
						return &tasksPerNode
					}(),
					UserId: ptr.To("1000"),
				},
			},
			wantErr: false,
		},
		{
			name: "Pod with bad annotation",
			args: args{
				client: fake.NewFakeClient(podWithBadAnnotation.DeepCopy()),
				ctx:    context.TODO(),
				pod:    podWithBadAnnotation.DeepCopy(),
			},
			want: &SlurmJobIR{
				RootPOM: metav1.PartialObjectMetadata{
					TypeMeta: pod_v1,
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testpod",
						Namespace: "default",
						Annotations: map[string]string{
							wellknown.AnnotationCpuPerTask: "NaN",
						},
						ResourceVersion: "999",
					},
				},
				Pods: corev1.PodList{
					Items: []corev1.Pod{*podWithBadAnnotation.DeepCopy()},
				},
				JobInfo: SlurmJobIRJobInfo{
					MaxNodes: func() *int32 {
						maxNodes := int32(1)
						return &maxNodes
					}(),
					TasksPerNode: func() *int32 {
						tasksPerNode := int32(1)
						return &tasksPerNode
					}(),
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TranslateToSlurmJobIR(tt.args.client, tt.args.ctx, tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("TranslateToSlurmJobIR() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("TranslateToSlurmJobIR() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseAnnotations(t *testing.T) {

	type args struct {
		slurmJobIR *SlurmJobIR
		anno       map[string]string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
		wantRes SlurmJobIR
	}{
		{
			name: "Empty",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno:       nil,
			},
			wantErr: false,
		},
		{
			name: "GoodAnnotations",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationAccount:     "slurm",
					wellknown.AnnotationConstraints: "foo",
					wellknown.AnnotationCpuPerTask:  "200m",
					wellknown.AnnotationGroupId:     "1000",
					wellknown.AnnotationJobName:     "jobname",
					wellknown.AnnotationLicenses:    "mathlib",
					wellknown.AnnotationMaxNodes:    "4",
					wellknown.AnnotationMemPerNode:  "1Gi",
					wellknown.AnnotationMinNodes:    "2",
					wellknown.AnnotationPartition:   "slurm-bridge",
					wellknown.AnnotationQOS:         "high",
					wellknown.AnnotationReservation: "training",
					wellknown.AnnotationTimeLimit:   "30",
					wellknown.AnnotationUserId:      "1000",
					wellknown.AnnotationWckey:       "key",
				},
			},
			wantErr: false,
			wantRes: SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					Account:     ptr.To("slurm"),
					Constraints: ptr.To("foo"),
					CpuPerTask:  ptr.To(int32(1)),
					GroupId:     ptr.To("1000"),
					JobName:     ptr.To("jobname"),
					Licenses:    ptr.To("mathlib"),
					MemPerNode:  ptr.To(int64(1024)),
					MinNodes:    ptr.To(int32(2)),
					MaxNodes:    ptr.To(int32(4)),
					Partition:   ptr.To("slurm-bridge"),
					QOS:         ptr.To("high"),
					Reservation: ptr.To("training"),
					TimeLimit:   ptr.To(int32(30)),
					UserId:      ptr.To("1000"),
					Wckey:       ptr.To("key"),
				},
			},
		},
		{
			name: "BadCpuPerTaskAnnotation",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationCpuPerTask: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadMemPerNodeAnnotation",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationMemPerNode: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadTimeLimitAnnotation",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationTimeLimit: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadNTasksAnnotation",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationMinNodes: "foo",
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseAnnotations(tt.args.slurmJobIR, tt.args.anno)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseAnnotations() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(&tt.wantRes, (tt.args.slurmJobIR)) {
				t.Errorf("parseAnnotations() error = %v, want %v", tt.wantRes, *(tt.args.slurmJobIR))
			}
		})
	}
}
