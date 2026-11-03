// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func Test_parseUserAnnotations(t *testing.T) {

	type args struct {
		component *SlurmJobComponent
		anno      map[string]string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
		wantRes SlurmJobComponent
	}{
		{
			name: "Empty",
			args: args{
				component: &SlurmJobComponent{},
				anno:      nil,
			},
			wantErr: false,
		},
		{
			name: "GoodAnnotations",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationAccount:     "slurm",
					wellknown.AnnotationConstraints: "foo",
					wellknown.AnnotationCpuPerTask:  "200m",
					wellknown.AnnotationGres:        "gres/gpu=2",
					wellknown.AnnotationGroupId:     "1000",
					wellknown.AnnotationJobName:     "jobname",
					wellknown.AnnotationLicenses:    "mathlib",
					wellknown.AnnotationMaxNodes:    "4",
					wellknown.AnnotationMemPerNode:  "1Gi",
					wellknown.AnnotationMinNodes:    "2",
					wellknown.AnnotationPartition:   "slurm-bridge",
					wellknown.AnnotationPriority:    "100",
					wellknown.AnnotationQOS:         "high",
					wellknown.AnnotationReservation: "training",
					wellknown.AnnotationTimeLimit:   "30",
					wellknown.AnnotationUserId:      "1000",
					wellknown.AnnotationWckey:       "key",
				},
			},
			wantErr: false,
			wantRes: SlurmJobComponent{
				JobInfo: SlurmJobIRJobInfo{
					Account:     ptr.To("slurm"),
					Constraints: ptr.To("foo"),
					CpuPerTask:  ptr.To(int32(1)),
					Gres:        ptr.To("gres/gpu=2"),
					GroupId:     ptr.To("1000"),
					JobName:     ptr.To("jobname"),
					Licenses:    ptr.To("mathlib"),
					MemPerNode:  ptr.To(int64(1024)),
					MinNodes:    ptr.To(int32(2)),
					MaxNodes:    ptr.To(int32(4)),
					Partition:   ptr.To("slurm-bridge"),
					Priority:    ptr.To(int32(100)),
					QOS:         ptr.To("high"),
					Reservation: ptr.To("training"),
					TimeLimit:   ptr.To(int32(30)),
					UserId:      ptr.To("1000"),
					Wckey:       ptr.To("key"),
				},
			},
		},
		{
			name: "TimeLimitDurationAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationTimeLimit: "2h",
				},
			},
			wantErr: false,
			wantRes: SlurmJobComponent{
				JobInfo: SlurmJobIRJobInfo{
					TimeLimit: ptr.To(int32(120)),
				},
			},
		},
		{
			name: "TimeLimitSlurmTimeAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationTimeLimit: "1-00:30:00",
				},
			},
			wantErr: false,
			wantRes: SlurmJobComponent{
				JobInfo: SlurmJobIRJobInfo{
					TimeLimit: ptr.To(int32(1470)),
				},
			},
		},
		{
			name: "TimeLimitSubMinuteAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationTimeLimit: "30s",
				},
			},
			wantErr: false,
			wantRes: SlurmJobComponent{
				JobInfo: SlurmJobIRJobInfo{
					TimeLimit: ptr.To(int32(1)),
				},
			},
		},
		{
			name: "BadCpuPerTaskAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationCpuPerTask: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadMaxNodesAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationMaxNodes: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadMemPerNodeAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationMemPerNode: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadTimeLimitAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationTimeLimit: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadTimeLimitDurationAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationTimeLimit: "1.5h",
				},
			},
			wantErr: true,
		},
		{
			name: "BadPriorityAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationPriority: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "BadNTasksAnnotation",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationMinNodes: "foo",
				},
			},
			wantErr: true,
		},
		{
			name: "Exclusive annotation false",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationExclusive: "false",
				},
			},
			wantErr: false,
			wantRes: SlurmJobComponent{
				JobInfo: SlurmJobIRJobInfo{
					Exclusive: ptr.To(false),
				},
			},
		},
		{
			name: "Exclusive annotation true",
			args: args{
				component: &SlurmJobComponent{},
				anno: map[string]string{
					wellknown.AnnotationExclusive: "true",
				},
			},
			wantErr: false,
			wantRes: SlurmJobComponent{
				JobInfo: SlurmJobIRJobInfo{
					Exclusive: ptr.To(true),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseUserAnnotations(tt.args.component, tt.args.anno)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseUserAnnotations() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(&tt.wantRes, tt.args.component) {
				t.Errorf("parseUserAnnotations() component = %v, want %v", *tt.args.component, tt.wantRes)
			}
		})
	}
}

// Annotation precedence is shared by bare Pods, legacy workloads and native
// PodGroups. Exercise the merged values, including invalid overridden defaults.
func TestPodAnnotationPrecedence(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "native"}[native], func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			tr := &translator{Reader: c, ctx: t.Context()}
			root := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				wellknown.AnnotationPriority: "invalid-default", wellknown.AnnotationAccount: "legacy-account",
				wellknown.AnnotationPartition: "legacy-partition",
			}}}
			var pg *PodGroup
			if native {
				pg = &PodGroup{}
			}
			pods := []corev1.Pod{
				{ObjectMeta: metav1.ObjectMeta{Name: "one", Annotations: map[string]string{wellknown.AnnotationPriority: "42", wellknown.AnnotationPartition: "pod-partition"}}},
				{ObjectMeta: metav1.ObjectMeta{Name: "two", Annotations: map[string]string{wellknown.AnnotationPriority: "invalid", wellknown.AnnotationPartition: "other-partition", wellknown.AnnotationQOS: "ignored"}}},
			}
			ir := &SlurmJobIR{Components: []SlurmJobComponent{{Pods: corev1.PodList{Items: pods}}}}
			if err := tr.applySlurmAnnotations(t.Context(), ir, root, pg); err != nil {
				t.Fatal(err)
			}
			info := ir.Components[0].JobInfo
			if ptr.Deref(info.Priority, 0) != 42 || ptr.Deref(info.Partition, "") != "pod-partition" ||
				ptr.Deref(info.Account, "") != "legacy-account" || info.QOS != nil {
				t.Fatalf("Pod overrides or nonconflicting defaults lost: %+v", info)
			}
		})
	}
}
