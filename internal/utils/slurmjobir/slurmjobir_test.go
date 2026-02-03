// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func podWithResources(cpuRequest, memoryRequest, cpuLimit, memoryLimit string) corev1.Pod {
	return corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(cpuRequest),
							corev1.ResourceMemory: resource.MustParse(memoryRequest),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(cpuLimit),
							corev1.ResourceMemory: resource.MustParse(memoryLimit),
						},
					},
				},
			},
		},
	}
}

func podWithGPU(gpuVendor, gpuQuantity string) corev1.Pod {
	return corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName(gpuVendor): resource.MustParse(gpuQuantity),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceName(gpuVendor): resource.MustParse(gpuQuantity),
						},
					},
				},
			},
		},
	}
}

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

func Test_parsePodsCpuAndMemory(t *testing.T) {
	type args struct {
		slurmJobIR *SlurmJobIR
	}
	tests := []struct {
		name       string
		args       args
		cpuPerTask *int32
		memPerNode *int64
	}{
		{
			name: "No requests or limits set",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{{}},
					},
				},
			},
			cpuPerTask: nil,
			memPerNode: nil,
		},
		{
			name: "requests set",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithResources("1", "100Mi", "2", "200Mi"),
						},
					},
					JobInfo: SlurmJobIRJobInfo{},
				},
			},
			cpuPerTask: ptr.To(int32(2)),
			memPerNode: ptr.To(int64(200)),
		},
		{
			name: "requests set on multiple pods",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithResources("1", "100Mi", "2", "400Mi"),
							{},
							podWithResources("8", "100Mi", "2", "200Mi"),
						},
					},
					JobInfo: SlurmJobIRJobInfo{},
				},
			},
			cpuPerTask: ptr.To(int32(8)),
			memPerNode: ptr.To(int64(400)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsePodsCpuAndMemory(tt.args.slurmJobIR)
			if !apiequality.Semantic.DeepEqual(tt.cpuPerTask, tt.args.slurmJobIR.JobInfo.CpuPerTask) {
				var gotCpu, wantCpu interface{}
				if tt.args.slurmJobIR.JobInfo.CpuPerTask != nil {
					gotCpu = *tt.args.slurmJobIR.JobInfo.CpuPerTask
				} else {
					gotCpu = nil
				}
				if tt.cpuPerTask != nil {
					wantCpu = *tt.cpuPerTask
				} else {
					wantCpu = nil
				}
				t.Errorf("parsePodsCpuAndMemory() CPU = %v, want %v", gotCpu, wantCpu)
			}
			if !apiequality.Semantic.DeepEqual(tt.memPerNode, tt.args.slurmJobIR.JobInfo.MemPerNode) {
				var gotMem, wantMem interface{}
				if tt.args.slurmJobIR.JobInfo.MemPerNode != nil {
					gotMem = *tt.args.slurmJobIR.JobInfo.MemPerNode
				} else {
					gotMem = nil
				}
				if tt.memPerNode != nil {
					wantMem = *tt.memPerNode
				} else {
					wantMem = nil
				}
				t.Errorf("parsePodsCpuAndMemory() Memory = %v, want %v", gotMem, wantMem)
			}
		})
	}
}

func Test_parseGPUDevicePlugin(t *testing.T) {
	type args struct {
		slurmJobIR *SlurmJobIR
	}
	tests := []struct {
		name string
		args args
		want *string
	}{
		{
			name: "No GPU requested",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{},
					},
				},
			},
			want: nil,
		},
		{
			name: "Single GPUs requested",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU("nvidia.com/gpu", "1"),
						},
					},
				},
			},
			want: ptr.To("gres/gpu=1"),
		},
		{
			name: "Multiple GPUs requested",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU("nvidia.com/gpu", "2"),
						},
					},
				},
			},
			want: ptr.To("gres/gpu=2"),
		},
		{
			name: "Multiple pods, multiple GPUs requested",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU("amd.com/gpu", "2"),
							podWithGPU("amd.com/gpu", "1"),
						},
					},
				},
			},
			want: ptr.To("gres/gpu=2"),
		},
		{
			name: "GPU requested via DRA Extended Resource Claim",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU(resourcev1.ResourceDeviceClassPrefix+"gpu.nvidia.com", "1"),
						},
					},
				},
			},
			want: ptr.To("gres/gpu:gpu.nvidia.com=1"),
		},
		{
			name: "Multiple GPU DRA Extended Resource Claims",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU(resourcev1.ResourceDeviceClassPrefix+"gpu.nvidia.com", "1"),
							podWithGPU(resourcev1.ResourceDeviceClassPrefix+"gpu.nvidia.com", "2"),
						},
					},
				},
			},
			want: ptr.To("gres/gpu:gpu.nvidia.com=2"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parseGPUDevicePlugin(tt.args.slurmJobIR)
			if !apiequality.Semantic.DeepEqual(tt.want, tt.args.slurmJobIR.JobInfo.Gres) {
				var gotGres, wantGres interface{}
				if tt.args.slurmJobIR.JobInfo.Gres != nil {
					gotGres = *tt.args.slurmJobIR.JobInfo.Gres
				} else {
					gotGres = nil
				}
				if tt.want != nil {
					wantGres = *tt.want
				} else {
					wantGres = nil
				}
				t.Errorf("parseGPUDevicePlugin() Gres = %v, want %v", gotGres, wantGres)
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
					wellknown.AnnotationGres:        "gres/gpu=2",
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
					Gres:        ptr.To("gres/gpu=2"),
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
			name: "BadMaxNodesAnnotation",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationMaxNodes: "foo",
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
		{
			name: "SharedSetToUser",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationShared: "user",
				},
			},
			wantErr: false,
			wantRes: SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					Shared: ptr.To("user"),
				},
			},
		},
		{
			name: "SharedSetToNone",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationShared: "none",
				},
			},
			wantErr: false,
			wantRes: SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					Shared: ptr.To("none"),
				},
			},
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
