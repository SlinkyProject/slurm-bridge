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
	resourcev1 "k8s.io/api/resource/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
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
			got, err := TranslateToSlurmJobIR(tt.args.client, dra.DefaultRegistry(), tt.args.ctx, tt.args.pod)
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

func TestTranslateToSlurmJobIRFallsBackFromForbiddenUnsupportedController(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	unsupportedGVK := schema.FromAPIVersionAndKind("example.com/v1", "ExampleController")
	const unsupportedName = "example-controller"

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: batchv1.SchemeGroupVersion.String(),
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "job1",
			Annotations: map[string]string{
				wellknown.AnnotationAccount: "job-account",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: unsupportedGVK.GroupVersion().String(),
					Kind:       unsupportedGVK.Kind,
					Name:       unsupportedName,
					Controller: ptr.To(true),
				},
			},
		},
	}
	pod := st.MakePod().Namespace("default").Name("pod1").Obj()
	pod.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: batchv1.SchemeGroupVersion.String(),
			Kind:       "Job",
			Name:       job.Name,
			Controller: ptr.To(true),
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(job, pod).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == unsupportedName {
					return apierrors.NewForbidden(
						unsupportedGVK.GroupVersion().WithResource("examplecontrollers").GroupResource(),
						unsupportedName,
						errors.New("access denied"),
					)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	got, err := TranslateToSlurmJobIR(cl, dra.DefaultRegistry(), context.TODO(), pod)
	if err != nil {
		t.Fatalf("TranslateToSlurmJobIR() error = %v", err)
	}
	if got.RootPOM.TypeMeta != job_v1 || got.RootPOM.Name != job.Name {
		t.Errorf("RootPOM = %v %q, want %v %q", got.RootPOM.TypeMeta, got.RootPOM.Name, job_v1, job.Name)
	}
	if got.JobInfo.MinNodes == nil || *got.JobInfo.MinNodes != 1 {
		t.Errorf("MinNodes = %v, want 1 from the Job controller", got.JobInfo.MinNodes)
	}
	if got.JobInfo.Account == nil || *got.JobInfo.Account != "job-account" {
		t.Errorf("Account = %v, want Job controller annotation", got.JobInfo.Account)
	}
}

func TestTranslateToSlurmJobIRPrefersSupportedWorkloadBelowReadableAncestor(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(jobset.AddToScheme(scheme))

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "outer-controller",
		},
	}
	jobSet := &jobset.JobSet{
		TypeMeta: jobSet_v1alpha2,
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "jobset1",
			Annotations: map[string]string{
				wellknown.AnnotationAccount: "jobset-account",
			},
			OwnerReferences: []metav1.OwnerReference{
				controllerOwner(deployment.APIVersion, deployment.Kind, deployment.Name),
			},
		},
	}
	job := &batchv1.Job{
		TypeMeta: job_v1,
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "job1",
			OwnerReferences: []metav1.OwnerReference{
				controllerOwner(jobSet.APIVersion, jobSet.Kind, jobSet.Name),
			},
		},
	}
	pod := st.MakePod().
		Namespace("default").
		Name("pod1").
		Label("job-name", job.Name).
		Obj()
	pod.OwnerReferences = []metav1.OwnerReference{
		controllerOwner(job.APIVersion, job.Kind, job.Name),
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployment, jobSet, job, pod).
		Build()

	got, err := TranslateToSlurmJobIR(cl, dra.DefaultRegistry(), context.TODO(), pod)
	if err != nil {
		t.Fatalf("TranslateToSlurmJobIR() error = %v", err)
	}
	if got.RootPOM.TypeMeta != jobSet_v1alpha2 || got.RootPOM.Name != jobSet.Name {
		t.Errorf("RootPOM = %v %q, want %v %q", got.RootPOM.TypeMeta, got.RootPOM.Name, jobSet_v1alpha2, jobSet.Name)
	}
	if got.JobInfo.Account == nil || *got.JobInfo.Account != "jobset-account" {
		t.Errorf("Account = %v, want JobSet controller annotation", got.JobInfo.Account)
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
		{
			name: "CPU DRA request sets CPUs per task",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU(cpuDRADeviceClassExtendedName, "4"),
						},
					},
					JobInfo: SlurmJobIRJobInfo{},
				},
			},
			cpuPerTask: ptr.To(int32(4)),
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

func TestTranslatorParseGPUResources(t *testing.T) {
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
			name: "Zero GPUs requested",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU("nvidia.com/gpu", "0"),
						},
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
			name: "CPU DRA Extended Resource Claim is ignored for GRES",
			args: args{
				slurmJobIR: &SlurmJobIR{
					Pods: corev1.PodList{
						Items: []corev1.Pod{
							podWithGPU(resourcev1.ResourceDeviceClassPrefix+"dra.cpu", "1"),
						},
					},
				},
			},
			want: nil,
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
	translator := translator{
		Reader:      fake.NewClientBuilder().Build(),
		ctx:         context.Background(),
		draRegistry: dra.DefaultRegistry(),
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := translator.parseGPUResources(tt.args.slurmJobIR); err != nil {
				t.Fatalf("translator.parseGPUResources() error = %v", err)
			}
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
				t.Errorf("translator.parseGPUResources() Gres = %v, want %v", gotGres, wantGres)
			}
		})
	}
}

func TestTranslatorParseGPUResourcesUsesDeviceProfile(t *testing.T) {
	const className = "example-gpus"
	deviceClass := &resourcev1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: className},
		Spec: resourcev1.DeviceClassSpec{
			Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == 'gpu.example.com'`},
			}},
		},
	}
	ir := &SlurmJobIR{Pods: corev1.PodList{Items: []corev1.Pod{
		podWithGPU(resourcev1.ResourceDeviceClassPrefix+className, "2"),
	}}}
	translator := translator{
		Reader:      fake.NewClientBuilder().WithObjects(deviceClass).Build(),
		ctx:         context.Background(),
		draRegistry: dra.DefaultRegistry(),
	}

	if err := translator.parseGPUResources(ir); err != nil {
		t.Fatalf("translator.parseGPUResources() error = %v", err)
	}
	if ir.JobInfo.Gres == nil || *ir.JobInfo.Gres != "gres/gpu:gpu-example=2" {
		t.Fatalf("translator.parseGPUResources() Gres = %v, want %q", ir.JobInfo.Gres, "gres/gpu:gpu-example=2")
	}
}

func TestTranslatorParseGPUResourcesUsesNVIDIADeviceProfile(t *testing.T) {
	const className = "gpu.nvidia.com"
	deviceClass := &resourcev1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: className},
		Spec: resourcev1.DeviceClassSpec{
			ExtendedResourceName: ptr.To(nvidiaDevicePlugin),
			Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'`},
			}},
		},
	}
	ir := &SlurmJobIR{Pods: corev1.PodList{Items: []corev1.Pod{
		podWithGPU(resourcev1.ResourceDeviceClassPrefix+className, "2"),
	}}}
	translator := translator{
		Reader:      fake.NewClientBuilder().WithObjects(deviceClass).Build(),
		ctx:         context.Background(),
		draRegistry: dra.DefaultRegistry(),
	}

	if err := translator.parseGPUResources(ir); err != nil {
		t.Fatalf("translator.parseGPUResources() error = %v", err)
	}
	if ir.JobInfo.Gres == nil || *ir.JobInfo.Gres != "gres/gpu:gpu-nvidia=2" {
		t.Fatalf("translator.parseGPUResources() Gres = %v, want %q", ir.JobInfo.Gres, "gres/gpu:gpu-nvidia=2")
	}
}

func TestTranslatorParseGPUResourcesKeepsNVIDIADevicePluginSeparateFromDRAAlias(t *testing.T) {
	deviceClass := &resourcev1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu.nvidia.com"},
		Spec: resourcev1.DeviceClassSpec{
			ExtendedResourceName: ptr.To(nvidiaDevicePlugin),
			Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'`},
			}},
		},
	}
	ir := &SlurmJobIR{Pods: corev1.PodList{Items: []corev1.Pod{
		podWithGPU(nvidiaDevicePlugin, "2"),
	}}}
	translator := translator{
		Reader:      fake.NewClientBuilder().WithObjects(deviceClass).Build(),
		ctx:         context.Background(),
		draRegistry: dra.DefaultRegistry(),
	}

	if err := translator.parseGPUResources(ir); err != nil {
		t.Fatalf("translator.parseGPUResources() error = %v", err)
	}
	if ir.JobInfo.Gres == nil || *ir.JobInfo.Gres != "gres/gpu=2" {
		t.Fatalf("translator.parseGPUResources() Gres = %v, want %q", ir.JobInfo.Gres, "gres/gpu=2")
	}
}

func TestTranslatorParseGPUResourcesCombinesProfileAliases(t *testing.T) {
	newClass := func(name string) *resourcev1.DeviceClass {
		return &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: resourcev1.DeviceClassSpec{
				Selectors: []resourcev1.DeviceSelector{{
					CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == 'gpu.example.com'`},
				}},
			},
		}
	}
	ir := &SlurmJobIR{
		Pods: corev1.PodList{
			Items: []corev1.Pod{{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
							corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "class-a"): resource.MustParse("1"),
							corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "class-b"): resource.MustParse("2"),
						}},
					}},
				},
			}},
		},
	}
	translator := translator{
		Reader:      fake.NewClientBuilder().WithObjects(newClass("class-a"), newClass("class-b")).Build(),
		ctx:         context.Background(),
		draRegistry: dra.DefaultRegistry(),
	}

	if err := translator.parseGPUResources(ir); err != nil {
		t.Fatalf("translator.parseGPUResources() error = %v", err)
	}
	if ir.JobInfo.Gres == nil || *ir.JobInfo.Gres != "gres/gpu:gpu-example=3" {
		t.Fatalf("translator.parseGPUResources() Gres = %v, want %q", ir.JobInfo.Gres, "gres/gpu:gpu-example=3")
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
					wellknown.AnnotationPriority:    "100",
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
			name: "BadPriorityAnnotation",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationPriority: "foo",
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
			name: "Exclusive annotation false",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationExclusive: "false",
				},
			},
			wantErr: false,
			wantRes: SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					Exclusive: ptr.To(false),
				},
			},
		},
		{
			name: "Exclusive annotation true",
			args: args{
				slurmJobIR: &SlurmJobIR{},
				anno: map[string]string{
					wellknown.AnnotationExclusive: "true",
				},
			},
			wantErr: false,
			wantRes: SlurmJobIR{
				JobInfo: SlurmJobIRJobInfo{
					Exclusive: ptr.To(true),
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
