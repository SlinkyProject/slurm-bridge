// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	tf "k8s.io/kubernetes/pkg/scheduler/testing/framework"
	"k8s.io/utils/ptr"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	kubeinterceptor "sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func slurmNode(name string, partitions ...string) types.V0044Node {
	return types.V0044Node{V0044Node: api.V0044Node{
		Name:       ptr.To(name),
		Partitions: ptr.To(api.V0044CsvString(partitions)),
	}}
}

type activateRecorder struct {
	pods map[string]*corev1.Pod
}

func mustRegisterTestWorkloadAPI(t *testing.T, scheme *runtime.Scheme, version string) *slurmjobir.WorkloadAPI {
	t.Helper()
	api, err := slurmjobir.RegisterWorkloadAPIVersion(scheme, version)
	if err != nil {
		t.Fatalf("RegisterWorkloadAPIVersion(): %v", err)
	}
	return api
}

func (r *activateRecorder) Activate(_ klog.Logger, pods map[string]*corev1.Pod) {
	r.pods = pods
}

func TestNewClientSchemeDefersWorkloadAPIRegistration(t *testing.T) {
	scheme, err := newClientScheme()
	if err != nil {
		t.Fatalf("newClientScheme(): %v", err)
	}
	for _, version := range []string{
		slurmjobir.WorkloadAPIVersionV1Alpha2,
		slurmjobir.WorkloadAPIVersionV1Beta1,
	} {
		gvk := schema.GroupVersion{Group: "scheduling.k8s.io", Version: version}.WithKind("PodGroup")
		if scheme.Recognizes(gvk) {
			t.Errorf("new client scheme unexpectedly recognizes %s", gvk)
		}
	}
}

func TestFindMatchingError(t *testing.T) {
	target := errors.New("target error")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
		},
		{
			name: "direct error",
			err:  target,
			want: true,
		},
		{
			name: "wrapped error",
			err:  fmt.Errorf("context: %w", target),
			want: true,
		},
		{
			name: "joined error",
			err:  errors.Join(errors.New("other error"), target),
			want: true,
		},
		{
			name: "nested error",
			err:  errors.Join(errors.New("other error"), fmt.Errorf("context: %w", target)),
			want: true,
		},
		{
			name: "no match",
			err:  errors.Join(errors.New("first error"), errors.New("second error")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findMatchingError(tt.err, func(err error) bool {
				return err.Error() == target.Error()
			})
			if (got != nil) != tt.want {
				t.Errorf("findMatchingError() = %v, want match %v", got, tt.want)
			}
		})
	}
}

type postFilterSlurmControl struct {
	deleteCalls          int
	deletedPod           *corev1.Pod
	getJobCalls          int
	getJobs              []*slurmcontrol.ExternalJob
	nodeNamesByPartition map[string][]string
	podToJobs            map[string]slurmcontrol.ExternalJob
	submitCalls          int
	submitIDs            []int32
	submittedIR          *slurmjobir.SlurmJobIR
	updateJob            func(*slurmjobir.SlurmJobIR) (int32, error)
}

func (c *postFilterSlurmControl) GetResources(context.Context, *corev1.Pod, string) (*slurmcontrol.NodeResources, error) {
	return &slurmcontrol.NodeResources{}, nil
}

func (c *postFilterSlurmControl) DeleteJob(_ context.Context, pod *corev1.Pod) error {
	c.deleteCalls++
	c.deletedPod = pod
	return nil
}

func (c *postFilterSlurmControl) GetJobsForPods(context.Context) (*map[string]slurmcontrol.ExternalJob, error) {
	if c.podToJobs != nil {
		return &c.podToJobs, nil
	}
	return &map[string]slurmcontrol.ExternalJob{}, nil
}

func (c *postFilterSlurmControl) GetJob(context.Context, *corev1.Pod) (*slurmcontrol.ExternalJob, error) {
	if c.getJobCalls < len(c.getJobs) {
		job := c.getJobs[c.getJobCalls]
		c.getJobCalls++
		return job, nil
	}
	return &slurmcontrol.ExternalJob{}, nil
}

func (c *postFilterSlurmControl) SubmitJob(_ context.Context, _ *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]int32, error) {
	c.submitCalls++
	c.submittedIR = ir
	if c.submitIDs != nil {
		return c.submitIDs, nil
	}
	return []int32{101, 102}, nil
}

func (c *postFilterSlurmControl) UpdateJob(_ context.Context, _ *corev1.Pod, ir *slurmjobir.SlurmJobIR) (int32, error) {
	if c.updateJob != nil {
		return c.updateJob(ir)
	}
	return 0, nil
}

func (c *postFilterSlurmControl) GetNodeNames(_ context.Context, partition *string) ([]string, error) {
	if c.nodeNamesByPartition != nil {
		partitionName := ""
		if partition != nil {
			partitionName = *partition
		}
		return c.nodeNamesByPartition[partitionName], nil
	}
	return []string{"node-a", "cpu-node", "gpu-node"}, nil
}

var _ slurmcontrol.SlurmControlInterface = (*postFilterSlurmControl)(nil)

func TestSlurmbridge_Name(t *testing.T) {
	tests := []struct {
		name string
		sb   *SlurmBridge
		want string
	}{
		{
			name: "Name is correct",
			sb:   &SlurmBridge{},
			want: Name,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{}
			if got := sb.Name(); got != tt.want {
				t.Errorf("Slurmbridge.Name() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	cs := clientsetfake.NewClientset()
	informerFactory := informers.NewSharedInformerFactory(cs, 0)
	registeredPlugins := []tf.RegisterPluginFunc{
		tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
		tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
	}
	f, err := tf.NewFramework(
		ctx,
		registeredPlugins,
		"slurm-bridge",
		fwkruntime.WithInformerFactory(informerFactory))
	if err != nil {
		t.Fatal(err)
	}
	type args struct {
		ctx    context.Context
		obj    runtime.Object
		handle fwk.Handle
	}
	tests := []struct {
		name    string
		args    args
		want    fwk.Plugin
		wantErr bool
	}{
		{
			name: "test initialization fails with no config",
			args: args{
				ctx:    ctx,
				obj:    nil,
				handle: f,
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New(tt.args.ctx, tt.args.obj, tt.args.handle)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("New() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlurmBridge_PreEnqueue(t *testing.T) {
	ctx := context.Background()
	pod := st.MakePod().Name("pod1").Obj()

	type fields struct {
		Client        kubeclient.Client
		schedulerName string
		slurmControl  slurmcontrol.SlurmControlInterface
		handle        fwk.Handle
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *fwk.Status
	}{
		{
			name: "Pod is patched with toleration",
			fields: fields{
				Client:       kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: nil,
			},
			args: args{
				ctx: ctx,
				pod: st.MakePod().Name("pod1").Obj(),
			},
			want: fwk.NewStatus(fwk.Success),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client:        tt.fields.Client,
				schedulerName: tt.fields.schedulerName,
				slurmControl:  tt.fields.slurmControl,
				handle:        tt.fields.handle,
			}
			got := sb.PreEnqueue(tt.args.ctx, tt.args.pod)
			if !apiequality.Semantic.DeepEqual(got.Reasons(), tt.want.Reasons()) {
				t.Errorf("SlurmBridge.PreEnqueue() got1.Reasons() = %v, want %v", got.Reasons(), tt.want.Reasons())
			}
			if tt.want.Code() == fwk.Success {
				found := false
				p := corev1.Pod{}
				_ = tt.fields.Client.Get(ctx, kubeclient.ObjectKeyFromObject(pod), &p)
				for _, toleration := range p.Spec.Tolerations {
					if apiequality.Semantic.DeepEqual(toleration, *utils.NewTolerationNodeBridged(sb.schedulerName)) {
						found = true
					}
				}
				if !found {
					t.Errorf("SlurmBridge.PreEnqueue() was a success but taint was not found.")
				}
			}
		})
	}
}

func TestSlurmBridge_PreFilter(t *testing.T) {
	ctx := context.Background()
	nodeInfo := []fwk.NodeInfo{
		framework.NewNodeInfo(),
	}
	nodeInfo[0].SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}})
	pod := st.MakePod().Name("pod1").Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj()
	cs := clientsetfake.NewClientset()
	informerFactory := informers.NewSharedInformerFactory(cs, 0)
	registeredPlugins := []tf.RegisterPluginFunc{
		tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
		tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
	}
	f, err := tf.NewFramework(
		ctx,
		registeredPlugins,
		"slurm-bridge",
		fwkruntime.WithInformerFactory(informerFactory))
	if err != nil {
		t.Fatal(err)
	}

	type fields struct {
		client        kubeclient.Client
		schedulerName string
		slurmControl  slurmcontrol.SlurmControlInterface
		handle        fwk.Handle
	}
	type args struct {
		ctx      context.Context
		state    fwk.CycleState
		pod      *corev1.Pod
		nodeinfo []fwk.NodeInfo
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *fwk.PreFilterResult
		want1  *fwk.Status
	}{
		{
			name: "JobId and Node assignment exist in annotations",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   context.Background(),
				state: framework.NewCycleState(),
				pod: st.MakePod().Name("pod1").Annotations(map[string]string{
					wellknown.AnnotationExternalJobNode: "node1",
				}).Labels(map[string]string{
					wellknown.LabelExternalJobId: "1"}).
					Obj(),
			},
			want:  &fwk.PreFilterResult{NodeNames: sets.New("node1")},
			want1: fwk.NewStatus(fwk.Success),
		},
		{
			name: "Error checking for Slurm job",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
							return ErrorNodeConfigInvalid
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Error, ErrorNodeConfigInvalid.Error()),
		},
		{
			name: "External job exists but nodes are not assigned",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To(""),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Success),
		},
		{
			name: "External job exists but nodes don't match",
			fields: fields{
				client: kubefake.NewFakeClient(
					pod.DeepCopy(),
				),
				schedulerName: "slurm-bridge-scheduler",
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Error, ErrorNoKubeNodeMatch.Error()),
		},
		{
			name: "External job exists",
			fields: fields{
				client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.NodeList{
						Items: []corev1.Node{
							{
								ObjectMeta: metav1.ObjectMeta{
									Name: "node1",
								},
							},
						}},
				),
				schedulerName: "slurm-bridge-scheduler",
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:      ctx,
				state:    framework.NewCycleState(),
				pod:      pod.DeepCopy(),
				nodeinfo: nodeInfo,
			},
			want:  &fwk.PreFilterResult{NodeNames: sets.New("node1")},
			want1: fwk.NewStatus(fwk.Success, ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client:        tt.fields.client,
				schedulerName: tt.fields.schedulerName,
				slurmControl:  tt.fields.slurmControl,
				handle:        tt.fields.handle,
				draRegistry:   dra.DefaultRegistry(),
			}
			got, got1 := sb.PreFilter(tt.args.ctx, tt.args.state, tt.args.pod, tt.args.nodeinfo)
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("SlurmBridge.PreFilter() got = %v, want %v", got, tt.want)
			}
			if got1.Code() != tt.want1.Code() {
				t.Errorf("SlurmBridge.PreFilter() got1.Code() = %v, want %v", got1.Code().String(), tt.want1.Code().String())
			}
			if !apiequality.Semantic.DeepEqual(got1.Reasons(), tt.want1.Reasons()) {
				t.Errorf("SlurmBridge.PreFilter() got1.Reasons() = %v, want %v", got1.Reasons(), tt.want1.Reasons())
			}
		})
	}
}

func TestSlurmBridge_PreFilterValidatesAllExternalJobPods(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(resourcev1.AddToScheme(scheme))
	workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Alpha2)

	const (
		namespace = "slurm-bridge"
		pgName    = "podgroup"
	)
	gpuResource := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "gpu.example.com")
	podA := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName + "-a"},
		Spec: corev1.PodSpec{
			SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(pgName)},
			Containers:      []corev1.Container{{Name: "valid"}},
		},
	}
	podB := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName + "-b"},
		Spec: corev1.PodSpec{
			SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(pgName)},
			Containers: []corev1.Container{
				{
					Name: "first",
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						gpuResource: resource.MustParse("1"),
					}},
				},
				{
					Name: "second",
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						gpuResource: resource.MustParse("1"),
					}},
				},
			},
		},
	}
	podGroup := &slurmjobir.PodGroup{
		TypeMeta: metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha2", Kind: "PodGroup"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      pgName,
		},
		Spec: slurmjobir.PodGroupSpec{
			SchedulingPolicy: schedulingv1alpha2.PodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
			},
		},
	}

	kubeClient := kubefake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(podA.DeepCopy(), podB.DeepCopy(), podGroup.DeepCopy(), exampleGPUDeviceClass("gpu.example.com")).
		Build()
	slurmClient := fake.NewClientBuilder().Build()
	sb := &SlurmBridge{
		Client:       kubeClient,
		slurmControl: slurmcontrol.NewControl(slurmClient, "kubernetes", "slurm-bridge"),
		draRegistry:  dra.DefaultRegistry(),
		workloadAPI:  workloadAPI,
	}

	got, status := sb.PreFilter(ctx, framework.NewCycleState(), podA.DeepCopy(), nil)
	if got != nil {
		t.Fatalf("PreFilter() result = %v, want nil", got)
	}
	if status.Code() != fwk.UnschedulableAndUnresolvable {
		t.Fatalf("PreFilter() status = %v, want UnschedulableAndUnresolvable: %v", status.Code(), status.Reasons())
	}
	wantReason := `pod slurm-bridge/podgroup-b: DRA DeviceClass "gpu.example.com" is requested by multiple containers "first" and "second"; slurm-bridge currently supports one requesting container per DeviceClass`
	if !apiequality.Semantic.DeepEqual(status.Reasons(), []string{wantReason}) {
		t.Fatalf("PreFilter() reasons = %v, want %q", status.Reasons(), wantReason)
	}
}

func TestSlurmBridge_PreFilterMarksAssignedPodGroupScheduled(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Alpha2)

	const (
		namespace = "slurm-bridge"
		pgName    = "podgroup"
		jobID     = int32(5)
	)
	podA := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      pgName + "-a",
			Labels:    map[string]string{wellknown.LabelExternalJobId: "5"},
			Annotations: map[string]string{
				wellknown.AnnotationExternalJobNode: "node1",
			},
		},
		Spec: corev1.PodSpec{
			SchedulingGroup: &corev1.PodSchedulingGroup{
				PodGroupName: ptr.To(pgName),
			},
		},
	}
	podB := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      pgName + "-b",
			Labels:    map[string]string{wellknown.LabelExternalJobId: "5"},
			Annotations: map[string]string{
				wellknown.AnnotationExternalJobNode: "node2",
			},
		},
		Spec: corev1.PodSpec{
			SchedulingGroup: &corev1.PodSchedulingGroup{
				PodGroupName: ptr.To(pgName),
			},
		},
	}
	podGroup := &slurmjobir.PodGroup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "scheduling.k8s.io/v1alpha2",
			Kind:       "PodGroup",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      pgName,
		},
		Spec: slurmjobir.PodGroupSpec{
			SchedulingPolicy: schedulingv1alpha2.PodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
			},
		},
	}

	kubeClient := kubefake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			podA.DeepCopy(),
			podB.DeepCopy(),
			podGroup.DeepCopy(),
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
		).
		WithStatusSubresource(&slurmjobir.PodGroup{}).
		Build()
	slurmControl := func() slurmcontrol.SlurmControlInterface {
		list := &types.V0044JobInfoList{
			Items: []types.V0044JobInfo{
				{V0044JobInfo: api.V0044JobInfo{
					AdminComment: func() *string {
						pi := externaljobinfo.ExternalJobInfo{
							Pods: []string{
								namespace + "/" + podA.Name,
								namespace + "/" + podB.Name,
							},
						}
						return ptr.To(pi.ToString())
					}(),
					JobId:    ptr.To(jobID),
					JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
					Nodes:    ptr.To("node[1-2]"),
				}},
			},
		}
		c := fake.NewClientBuilder().
			WithLists(list).
			Build()
		return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
	}()
	sb := &SlurmBridge{
		Client:        kubeClient,
		schedulerName: "slurm-bridge-scheduler",
		slurmControl:  slurmControl,
		draRegistry:   dra.DefaultRegistry(),
		workloadAPI:   workloadAPI,
	}

	got, status := sb.PreFilter(ctx, framework.NewCycleState(), podA.DeepCopy(), nil)
	if status.Code() != fwk.Success {
		t.Fatalf("PreFilter() status = %v, want Success: %v", status.Code(), status.Reasons())
	}
	if !apiequality.Semantic.DeepEqual(got, &fwk.PreFilterResult{NodeNames: sets.New("node1")}) {
		t.Fatalf("PreFilter() result = %v, want node1", got)
	}

	updated := &slurmjobir.PodGroup{TypeMeta: podGroup.TypeMeta}
	if err := kubeClient.Get(ctx, kubeclient.ObjectKey{Namespace: namespace, Name: pgName}, updated); err != nil {
		t.Fatalf("Get PodGroup: %v", err)
	}
	condition := apimeta.FindStatusCondition(updated.Status.Conditions, workloadAPI.ScheduledCondition)
	if condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("PodGroupScheduled condition = %#v, want true", condition)
	}
}

func TestAllocatedNodeRejectedByKubernetes(t *testing.T) {
	allocatedJob := &slurmcontrol.ExternalJob{JobId: 1, Nodes: "node1"}
	assignedPod := st.MakePod().Name("pod1").Annotations(map[string]string{
		wellknown.AnnotationExternalJobNode: "node1",
	}).Obj()
	tests := []struct {
		name string
		pod  *corev1.Pod
		job  *slurmcontrol.ExternalJob
		m    fwk.NodeToStatusReader
		want bool
	}{
		{
			name: "allocation happened after PreFilter",
			pod:  st.MakePod().Name("pod1").Obj(),
			job:  allocatedJob,
			m: framework.NewNodeToStatus(map[string]*fwk.Status{
				"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
			}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			want: false,
		},
		{
			name: "SlurmBridge rejected node",
			pod:  assignedPod,
			job:  allocatedJob,
			m: framework.NewNodeToStatus(map[string]*fwk.Status{
				"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
			}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			want: false,
		},
		{
			name: "Kubernetes plugin rejected allocated node",
			pod:  assignedPod,
			job:  allocatedJob,
			m: framework.NewNodeToStatus(map[string]*fwk.Status{
				"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin("OtherPlugin"),
			}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allocatedNodeRejectedByKubernetes(tt.pod, tt.job, tt.m); got != tt.want {
				t.Errorf("allocatedNodeRejectedByKubernetes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlurmBridge_PostFilter(t *testing.T) {
	ctx := context.Background()
	pod := st.MakePod().Name("pod1").Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj()
	allocatedPod := pod.DeepCopy()
	allocatedPod.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node1"}
	cs := clientsetfake.NewClientset()
	informerFactory := informers.NewSharedInformerFactory(cs, 0)
	registeredPlugins := []tf.RegisterPluginFunc{
		tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
		tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
	}
	activator := &activateRecorder{}
	f, err := tf.NewFramework(
		ctx,
		registeredPlugins,
		"slurm-bridge",
		fwkruntime.WithInformerFactory(informerFactory),
		fwkruntime.WithPodActivator(activator),
		fwkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(
			[]*corev1.Pod{
				pod,
			},
			[]*corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
			})))
	if err != nil {
		t.Fatal(err)
	}

	type fields struct {
		Client        kubeclient.Client
		schedulerName string
		slurmControl  slurmcontrol.SlurmControlInterface
		handle        fwk.Handle
	}
	type args struct {
		ctx   context.Context
		state fwk.CycleState
		pod   *corev1.Pod
		m     fwk.NodeToStatusReader
	}
	newUpdateRaceSlurmControl := func(nodesAfterUpdate string) slurmcontrol.SlurmControlInterface {
		nodes := &types.V0044NodeList{
			Items: []types.V0044Node{
				slurmNode("node1", "slurm-bridge"),
				slurmNode("node2", "slurm-bridge"),
			},
		}
		base := fake.NewClientBuilder().
			WithLists(nodes).
			Build()
		jobGets := 0
		f := interceptor.Funcs{
			Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
				job, ok := obj.(*types.V0044JobInfo)
				if !ok {
					return base.Get(ctx, key, obj, opts...)
				}

				jobGets++
				state := api.V0044JobInfoJobStatePENDING
				nodes := ""
				if jobGets > 1 {
					state = api.V0044JobInfoJobStateRUNNING
					nodes = nodesAfterUpdate
				}
				*job = types.V0044JobInfo{V0044JobInfo: api.V0044JobInfo{
					JobId:    ptr.To(int32(1)),
					JobState: &[]api.V0044JobInfoJobState{state},
					Nodes:    ptr.To(nodes),
				}}
				return nil
			},
			Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
				return errors.Join(
					errors.New("Internal Server Error"),
					errors.New("Job is no longer pending execution"),
				)
			},
		}
		return slurmcontrol.NewControl(interceptor.NewClient(base, f), "kubernetes", "slurm-bridge")
	}
	tests := []struct {
		name         string
		fields       fields
		args         args
		want         *fwk.PostFilterResult
		want1        *fwk.Status
		wantPodNode  string
		wantPodJobID *string
		wantActivate bool
	}{
		{
			name: "Error checking for Slurm job",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
							return ErrorNodeConfigInvalid
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Error, ErrorNodeConfigInvalid.Error()),
		},
		{
			name: "Allocated node rejected by Kubernetes",
			fields: fields{
				Client: kubefake.NewFakeClient(
					allocatedPod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					jobs := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{Pods: []string{"/pod1"}}
									return ptr.To(pi.ToString())
								}(),
							}},
						},
					}
					c := fake.NewClientBuilder().WithLists(jobs).Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   allocatedPod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin("OtherPlugin"),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:         nil,
			want1:        fwk.NewStatus(fwk.Success),
			wantPodJobID: ptr.To(""),
			wantActivate: true,
		},
		{
			name: "Error listing Slurm nodes",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						List: func(ctx context.Context, list object.ObjectList, opts ...slurmclient.ListOption) error {
							return ErrorNodeConfigInvalid
						},
					}
					return slurmcontrol.NewControl(interceptor.NewClient(fake.NewFakeClient(), f), "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Error, ErrorNodeConfigInvalid.Error()),
		},
		{
			name: "Kube nodes not valid slurm nodes",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.CreateOption) error {
							obj.(*types.V0044JobInfo).JobId = ptr.To(int32(1))
							return nil
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Success),
		},
		{
			name: "Creating an external job fails with invalid node config",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, object object.Object, req any, opts ...slurmclient.CreateOption) error {
							return errors.Join(errors.New("Bad Request"), ErrorNodeConfigInvalid)
						},
					}
					nodes := &types.V0044NodeList{
						Items: []types.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(nodes).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorNodeConfigInvalid.Error()),
		},
		{
			name: "Creating an external job fails",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, object object.Object, req any, opts ...slurmclient.CreateOption) error {
							return ErrorPodUpdateFailed
						},
					}
					nodes := &types.V0044NodeList{
						Items: []types.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(nodes).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Error, ErrorPodUpdateFailed.Error()),
		},
		{
			name: "Creating an external job excludes only infeasible nodes in job partition",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.CreateOption) error {
							jobSubmit := req.(api.V0044JobSubmitReq)
							want := ptr.To(api.V0044CsvString{"node2"})
							if !reflect.DeepEqual(jobSubmit.Job.ExcludedNodes, want) {
								return fmt.Errorf("ExcludedNodes = %v, want %v", jobSubmit.Job.ExcludedNodes, want)
							}
							obj.(*types.V0044JobInfo).JobId = ptr.To(int32(1))
							return nil
						},
					}
					nodes := &types.V0044NodeList{
						Items: []types.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
							slurmNode("node3", "other"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(nodes).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin("OtherPlugin"),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:         nil,
			want1:        fwk.NewStatus(fwk.Success),
			wantActivate: true,
		},
		{
			name: "Creating an external job succeeds",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(_ context.Context, obj object.Object, _ any, _ ...slurmclient.CreateOption) error {
							obj.(*types.V0044JobInfo).JobId = ptr.To(int32(1))
							return nil
						},
					}
					nodes := &types.V0044NodeList{
						Items: []types.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(nodes).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:         nil,
			want1:        fwk.NewStatus(fwk.Success),
			wantActivate: true,
		},
		{
			name: "Updating an external job succeeds",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
							jobUpdate := req.(api.V0044JobDescMsg)
							want := ptr.To(api.V0044CsvString{})
							if !reflect.DeepEqual(jobUpdate.ExcludedNodes, want) {
								return fmt.Errorf("ExcludedNodes = %v, want empty list", jobUpdate.ExcludedNodes)
							}
							if jobUpdate.RequiredNodes != nil {
								return fmt.Errorf("RequiredNodes = %v, want nil", jobUpdate.RequiredNodes)
							}
							return nil
						},
					}
					jobs := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
								Nodes:    ptr.To(""),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}()},
							},
						},
					}
					nodes := &types.V0044NodeList{
						Items: []types.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(jobs, nodes).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:         nil,
			want1:        fwk.NewStatus(fwk.Success, ErrorNoNodesAssigned.Error()),
			wantActivate: true,
		},
		{
			name: "Updating an external job fails",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
							return errors.Join(ErrorPodUpdateFailed)
						},
					}
					jobs := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
								Nodes:    ptr.To(""),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}()},
							},
						},
					}
					nodes := &types.V0044NodeList{
						Items: []types.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(jobs, nodes).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Error, ErrorPodUpdateFailed.Error()),
		},
		{
			name: "Updating an external job races with Slurm allocation",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: newUpdateRaceSlurmControl("node1"),
				handle:       f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:         nil,
			want1:        fwk.NewStatus(fwk.Success),
			wantPodNode:  "node1",
			wantActivate: true,
		},
		{
			name: "Updating an external job races but Slurm has no allocated nodes",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: newUpdateRaceSlurmControl(""),
				handle:       f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:         nil,
			want1:        fwk.NewStatus(fwk.Success),
			wantActivate: true,
		},
		{
			name: "Non-pending external job with no nodes skips update",
			fields: fields{
				Client: kubefake.NewFakeClient(
					pod.DeepCopy(),
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}},
				),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
							return errors.Join(ErrorPodUpdateFailed)
						},
					}
					jobs := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To(""),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}()},
							},
						},
					}
					nodes := &types.V0044NodeList{
						Items: []types.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(jobs, nodes).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
				m: framework.NewNodeToStatus(map[string]*fwk.Status{
					"node1": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
					"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
				}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)),
			},
			want:         nil,
			want1:        fwk.NewStatus(fwk.Success),
			wantActivate: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activator.pods = nil
			sb := &SlurmBridge{
				Client:        tt.fields.Client,
				schedulerName: tt.fields.schedulerName,
				slurmControl:  tt.fields.slurmControl,
				handle:        tt.fields.handle,
				draRegistry:   dra.DefaultRegistry(),
			}
			s := &stateData{}
			s.slurmJobIR, _ = slurmjobir.TranslateToSlurmJobIR(tt.fields.Client, sb.draRegistry, sb.workloadAPI, tt.args.ctx, tt.args.pod)
			tt.args.state.Write(stateKey, s)
			got, got1 := sb.PostFilter(tt.args.ctx, tt.args.state, tt.args.pod, tt.args.m)
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("SlurmBridge.PostFilter() got = %v, want %v", got, tt.want)
			}
			if got1.Code() != tt.want1.Code() {
				t.Errorf("SlurmBridge.PostFilter() got1.Code() = %v, want %v", got1.Code().String(), tt.want1.Code().String())
			}
			if !apiequality.Semantic.DeepEqual(got1.Reasons(), tt.want1.Reasons()) {
				t.Errorf("SlurmBridge.PostFilter() got1.Reasons() = %v, want %v", got1.Reasons(), tt.want1.Reasons())
			}
			if gotActivate := len(activator.pods) > 0; gotActivate != tt.wantActivate {
				t.Errorf("SlurmBridge.PostFilter() activated pod = %v, want %v", gotActivate, tt.wantActivate)
			}
			if tt.wantPodNode != "" {
				gotPod := &corev1.Pod{}
				if err := tt.fields.Client.Get(tt.args.ctx, kubeclient.ObjectKeyFromObject(tt.args.pod), gotPod); err != nil {
					t.Errorf("SlurmBridge.PostFilter() failed to get pod after PostFilter = %v", err)
				}
				if gotPod.Annotations[wellknown.AnnotationExternalJobNode] != tt.wantPodNode {
					t.Errorf("SlurmBridge.PostFilter() pod node annotation = %v, want %v", gotPod.Annotations[wellknown.AnnotationExternalJobNode], tt.wantPodNode)
				}
			}
			if tt.wantPodJobID != nil {
				gotPod := &corev1.Pod{}
				if err := tt.fields.Client.Get(tt.args.ctx, kubeclient.ObjectKeyFromObject(tt.args.pod), gotPod); err != nil {
					t.Errorf("SlurmBridge.PostFilter() failed to get pod after PostFilter = %v", err)
				}
				if gotJobID := gotPod.Labels[wellknown.LabelExternalJobId]; gotJobID != *tt.wantPodJobID {
					t.Errorf("SlurmBridge.PostFilter() pod job ID = %q, want %q", gotJobID, *tt.wantPodJobID)
				}
			}
		})
	}
}

func TestSlurmBridge_PreFilterExtensions(t *testing.T) {
	type fields struct {
		client       kubeclient.Client
		slurmControl slurmcontrol.SlurmControlInterface
		handle       fwk.Handle
	}
	tests := []struct {
		name   string
		fields fields
		want   fwk.PreFilterExtensions
	}{
		{
			name:   "PreFilterExtension returns",
			fields: fields{},
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client:       tt.fields.client,
				slurmControl: tt.fields.slurmControl,
				handle:       tt.fields.handle,
			}
			if got := sb.PreFilterExtensions(); !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("SlurmBridge.PreFilterExtensions() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlurmBridge_Filter(t *testing.T) {
	ctx := context.Background()
	nodeInfoFor := func(node *corev1.Node) *framework.NodeInfo {
		nodeInfo := framework.NewNodeInfo()
		nodeInfo.SetNode(node)
		return nodeInfo
	}
	nodeInfo := nodeInfoFor(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}})
	podWithAnnotation := st.MakePod().Name("foo").Annotations(map[string]string{wellknown.AnnotationExternalJobNode: "node1"}).Obj()
	podWithoutAnnotation := st.MakePod().Name("foo").Obj()
	type fields struct {
		client       kubeclient.Client
		slurmControl slurmcontrol.SlurmControlInterface
		handle       fwk.Handle
	}
	type args struct {
		ctx      context.Context
		state    *framework.CycleState
		pod      *corev1.Pod
		nodeInfo *framework.NodeInfo
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *fwk.Status
	}{
		{
			name: "Node in annotation matches",
			fields: fields{
				client: nil,
				slurmControl: slurmcontrol.NewControl(
					fake.NewFakeClient(), "kubernetes", "slurm-bridge"),
			},
			args: args{
				ctx:      ctx,
				state:    nil,
				pod:      podWithAnnotation.DeepCopy(),
				nodeInfo: nodeInfo,
			},
			want: fwk.NewStatus(fwk.Success, ""),
		},
		{
			name: "Node in annotation does not match",
			fields: fields{
				client:       nil,
				slurmControl: slurmcontrol.NewControl(fake.NewFakeClient(), "kubernetes", "slurm-bridge"),
			},
			args: args{
				ctx:      ctx,
				state:    nil,
				pod:      podWithoutAnnotation.DeepCopy(),
				nodeInfo: nodeInfo,
			},
			want: fwk.NewStatus(fwk.Unschedulable, "node does not match annotation"),
		},
		{
			name: "GRES compatibility condition is informational",
			fields: fields{
				slurmControl: slurmcontrol.NewControl(fake.NewFakeClient(), "kubernetes", "slurm-bridge"),
			},
			args: args{
				ctx:   ctx,
				state: nil,
				pod:   podWithAnnotation.DeepCopy(),
				nodeInfo: nodeInfoFor(&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "node1"},
					Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
						Type:    wellknown.NodeConditionSlurmGRESCompatible,
						Status:  corev1.ConditionFalse,
						Reason:  "IncompatibleSlurmGRES",
						Message: "Slurm GRES does not match DRA inventory",
					}},
					},
				}),
			},
			want: fwk.NewStatus(fwk.Success, ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client:       tt.fields.client,
				slurmControl: tt.fields.slurmControl,
				handle:       tt.fields.handle,
			}
			got := sb.Filter(tt.args.ctx, tt.args.state, tt.args.pod, tt.args.nodeInfo)
			if got.Code() != tt.want.Code() {
				t.Errorf("SlurmBridge.Filter() got1.Code() = %v, want %v", got.Code().String(), tt.want.Code().String())
			}
			if !apiequality.Semantic.DeepEqual(got.Reasons(), tt.want.Reasons()) {
				t.Errorf("SlurmBridge.Filter() got1.Reasons() = %v, want %v", got.Reasons(), tt.want.Reasons())
			}
		})
	}
}

func TestSlurmBridge_deleteExternalJob(t *testing.T) {
	pod := st.MakePod().Name("pod1").Annotations(
		map[string]string{wellknown.AnnotationExternalJobNode: "node1"}).Labels(
		map[string]string{wellknown.LabelExternalJobId: "1"}).Obj()
	cs := clientsetfake.NewClientset()
	informerFactory := informers.NewSharedInformerFactory(cs, 0)
	registeredPlugins := []tf.RegisterPluginFunc{
		tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
		tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
	}
	f, err := tf.NewFramework(
		context.Background(),
		registeredPlugins,
		"slurm-bridge",
		fwkruntime.WithInformerFactory(informerFactory))
	if err != nil {
		t.Fatal(err)
	}
	type fields struct {
		Client       kubeclient.Client
		slurmControl slurmcontrol.SlurmControlInterface
		handle       fwk.Handle
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "Delete fails on job that does not exist",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: slurmcontrol.NewControl(
					fake.NewFakeClient(), "kubernetes", "slurm-bridge"),
				handle: f,
			},
			args: args{
				ctx: context.Background(),
				pod: pod.DeepCopy(),
			},
			wantErr: true,
		},
		{
			name: "External job is deleted",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								JobId: ptr.To[int32](1),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx: context.Background(),
				pod: pod.DeepCopy(),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client:       tt.fields.Client,
				slurmControl: tt.fields.slurmControl,
				handle:       tt.fields.handle,
				draRegistry:  dra.DefaultRegistry(),
			}
			if err := sb.deleteExternalJob(tt.args.ctx, tt.args.pod); (err != nil) != tt.wantErr {
				t.Errorf("SlurmBridge.deleteExternalJob() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSlurmBridge_validatePodToJob(t *testing.T) {
	pod := st.MakePod().Name("pod1").Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj()
	type fields struct {
		Client       kubeclient.Client
		slurmControl slurmcontrol.SlurmControlInterface
		handle       fwk.Handle
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *corev1.Pod
		wantErr bool
	}{
		{
			name: "Fail to get jobs",
			fields: fields{
				Client: kubefake.NewFakeClient(),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						List: func(ctx context.Context, list object.ObjectList, opts ...slurmclient.ListOption) error {
							return ErrorNoKubeNode
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: nil,
			},
			args: args{
				ctx: context.TODO(),
				pod: pod.DeepCopy(),
			},
			want:    pod.DeepCopy(),
			wantErr: true,
		},
		{
			name: "Matching slurm job exists",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId: ptr.To[int32](1),
								Nodes: ptr.To(""),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: nil,
			},
			args: args{
				ctx: context.TODO(),
				pod: pod.DeepCopy(),
			},
			want: func() *corev1.Pod {
				want := pod.DeepCopy()
				want.Finalizers = append(want.Finalizers, wellknown.FinalizerScheduler)
				return want
			}(),
			wantErr: false,
		},
		{
			name: "Matching slurm job does not exist but patch fails",
			fields: fields{
				Client: kubefake.NewFakeClient(),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId: ptr.To[int32](2),
								Nodes: ptr.To(""),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: nil,
			},
			args: args{
				ctx: context.TODO(),
				pod: pod.DeepCopy(),
			},
			want:    pod.DeepCopy(),
			wantErr: true,
		},
		{
			name: "Matching slurm job does not exist",
			fields: fields{
				Client: kubefake.NewFakeClient(pod),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0044JobInfoList{
						Items: []types.V0044JobInfo{
							{V0044JobInfo: api.V0044JobInfo{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId: ptr.To[int32](2),
								Nodes: ptr.To(""),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
				handle: nil,
			},
			args: args{
				ctx: context.TODO(),
				pod: func() *corev1.Pod {
					pod.Annotations = map[string]string{
						wellknown.AnnotationExternalJobNode: "node2",
					}
					return pod.DeepCopy()
				}(),
			},
			want: func() *corev1.Pod {
				pod.Annotations = map[string]string{
					wellknown.AnnotationExternalJobNode: "",
				}
				pod.Labels = map[string]string{
					wellknown.LabelExternalJobId: "2",
				}
				pod.Finalizers = []string{
					wellknown.FinalizerScheduler,
				}
				return pod.DeepCopy()
			}(),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client:       tt.fields.Client,
				slurmControl: tt.fields.slurmControl,
				handle:       tt.fields.handle,
			}
			if err := sb.validatePodToJob(tt.args.ctx, tt.args.pod); (err != nil) != tt.wantErr {
				t.Errorf("SlurmBridge.validatePodToJob() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !apiequality.Semantic.DeepEqual(tt.args.pod, tt.want) {
				t.Errorf("SlurmBridge.validatePodToJob() pod = %v, want %v", tt.args.pod, tt.want)
			}
		})
	}
}

func TestSlurmBridge_validatePodToJobReconcilesIdentity(t *testing.T) {
	tests := []struct {
		name          string
		labels        map[string]string
		finalizers    []string
		job           slurmcontrol.ExternalJob
		wantLabels    map[string]string
		wantFinalizer bool
		wantPatches   int
	}{
		{
			name: "does not adopt unlabeled pod",
			job: slurmcontrol.ExternalJob{
				JobId:    102,
				HetJobId: 100,
			},
		},
		{
			name: "corrects zero component label",
			labels: map[string]string{
				wellknown.LabelExternalJobId: "0",
			},
			job: slurmcontrol.ExternalJob{
				JobId: 102,
			},
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId: "102",
			},
			wantFinalizer: true,
			wantPatches:   1,
		},
		{
			name: "corrects zero component label with het jobid",
			labels: map[string]string{
				wellknown.LabelExternalJobId: "0",
			},
			job: slurmcontrol.ExternalJob{
				JobId:    102,
				HetJobId: 100,
			},
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			wantFinalizer: true,
			wantPatches:   1,
		},
		{
			name: "corrects stale component and base labels",
			labels: map[string]string{
				wellknown.LabelExternalJobId:    "999",
				wellknown.LabelExternalHetJobId: "998",
			},
			job: slurmcontrol.ExternalJob{
				JobId:    102,
				HetJobId: 100,
			},
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			wantFinalizer: true,
			wantPatches:   1,
		},
		{
			name: "removes stale base label from homogeneous job",
			labels: map[string]string{
				wellknown.LabelExternalJobId:    "101",
				wellknown.LabelExternalHetJobId: "100",
			},
			finalizers: []string{wellknown.FinalizerScheduler},
			job: slurmcontrol.ExternalJob{
				JobId: 101,
			},
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId: "101",
			},
			wantFinalizer: true,
			wantPatches:   1,
		},
		{
			name: "matching identity is a no-op",
			labels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			finalizers: []string{wellknown.FinalizerScheduler},
			job: slurmcontrol.ExternalJob{
				JobId:    102,
				HetJobId: 100,
			},
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			wantFinalizer: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace:  "workload",
				Name:       "pod-a",
				Labels:     tt.labels,
				Finalizers: tt.finalizers,
			}}
			patches := 0
			kubeClient := kubefake.NewClientBuilder().
				WithObjects(pod.DeepCopy()).
				WithInterceptorFuncs(kubeinterceptor.Funcs{
					Patch: func(
						ctx context.Context,
						c kubeclient.WithWatch,
						obj kubeclient.Object,
						patch kubeclient.Patch,
						opts ...kubeclient.PatchOption,
					) error {
						patches++
						return c.Patch(ctx, obj, patch, opts...)
					},
				}).
				Build()
			key := kubeclient.ObjectKeyFromObject(pod).String()
			control := &postFilterSlurmControl{
				podToJobs: map[string]slurmcontrol.ExternalJob{key: tt.job},
			}
			sb := &SlurmBridge{Client: kubeClient, slurmControl: control}

			if err := sb.validatePodToJob(ctx, pod); err != nil {
				t.Fatalf("validatePodToJob() error = %v, want nil", err)
			}
			got := &corev1.Pod{}
			if err := kubeClient.Get(ctx, kubeclient.ObjectKeyFromObject(pod), got); err != nil {
				t.Fatalf("Get(%s) error = %v, want nil", pod.Name, err)
			}
			if !apiequality.Semantic.DeepEqual(got.Labels, tt.wantLabels) {
				t.Errorf("validatePodToJob() labels = %v, want %v", got.Labels, tt.wantLabels)
			}
			if gotFinalizer := slices.Contains(got.Finalizers, wellknown.FinalizerScheduler); gotFinalizer != tt.wantFinalizer {
				t.Errorf("validatePodToJob() finalizer present = %t, want %t", gotFinalizer, tt.wantFinalizer)
			}
			if patches != tt.wantPatches {
				t.Errorf("validatePodToJob() Patch calls = %d, want %d", patches, tt.wantPatches)
			}
			if !apiequality.Semantic.DeepEqual(pod.Labels, tt.wantLabels) {
				t.Errorf("validatePodToJob() in-memory labels = %v, want %v", pod.Labels, tt.wantLabels)
			}
		})
	}
}

func TestSlurmBridge_labelPodsWithJobIdReconcilesState(t *testing.T) {
	tests := []struct {
		name        string
		labels      map[string]string
		finalizers  []string
		jobID       int32
		hetJobID    int32
		wantLabels  map[string]string
		wantPatches int
	}{
		{
			name:     "persists base identity before component discovery",
			hetJobID: 100,
			wantLabels: map[string]string{
				wellknown.LabelExternalHetJobId: "100",
			},
			wantPatches: 1,
		},
		{
			name:     "labels heterogeneous pod",
			jobID:    102,
			hetJobID: 100,
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			wantPatches: 1,
		},
		{
			name: "corrects stale base label",
			labels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "999",
			},
			finalizers: []string{wellknown.FinalizerScheduler},
			jobID:      102,
			hetJobID:   100,
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			wantPatches: 1,
		},
		{
			name: "removes stale base label from homogeneous pod",
			labels: map[string]string{
				wellknown.LabelExternalJobId:    "101",
				wellknown.LabelExternalHetJobId: "100",
			},
			finalizers: []string{wellknown.FinalizerScheduler},
			jobID:      101,
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId: "101",
			},
			wantPatches: 1,
		},
		{
			name: "restores missing finalizer",
			labels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			jobID:    102,
			hetJobID: 100,
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			wantPatches: 1,
		},
		{
			name: "matching state is a no-op",
			labels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
			finalizers: []string{wellknown.FinalizerScheduler},
			jobID:      102,
			hetJobID:   100,
			wantLabels: map[string]string{
				wellknown.LabelExternalJobId:    "102",
				wellknown.LabelExternalHetJobId: "100",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace:  "workload",
				Name:       "pod-a",
				Labels:     tt.labels,
				Finalizers: tt.finalizers,
			}}
			patches := 0
			kubeClient := kubefake.NewClientBuilder().
				WithObjects(pod.DeepCopy()).
				WithInterceptorFuncs(kubeinterceptor.Funcs{
					Patch: func(
						ctx context.Context,
						c kubeclient.WithWatch,
						obj kubeclient.Object,
						patch kubeclient.Patch,
						opts ...kubeclient.PatchOption,
					) error {
						patches++
						return c.Patch(ctx, obj, patch, opts...)
					},
				}).
				Build()
			sb := &SlurmBridge{Client: kubeClient}
			component := slurmjobir.SlurmJobComponent{
				Pods: corev1.PodList{Items: []corev1.Pod{*pod.DeepCopy()}},
			}

			if err := sb.labelPodsWithJobId(ctx, tt.jobID, tt.hetJobID, component); err != nil {
				t.Fatalf("labelPodsWithJobId() error = %v, want nil", err)
			}
			got := &corev1.Pod{}
			if err := kubeClient.Get(ctx, kubeclient.ObjectKeyFromObject(pod), got); err != nil {
				t.Fatalf("Get(%s) error = %v, want nil", pod.Name, err)
			}
			if !apiequality.Semantic.DeepEqual(got.Labels, tt.wantLabels) {
				t.Errorf("labelPodsWithJobId() labels = %v, want %v", got.Labels, tt.wantLabels)
			}
			if !apiequality.Semantic.DeepEqual(got.Finalizers, []string{wellknown.FinalizerScheduler}) {
				t.Errorf("labelPodsWithJobId() finalizers = %v, want scheduler finalizer", got.Finalizers)
			}
			if patches != tt.wantPatches {
				t.Errorf("labelPodsWithJobId() Patch calls = %d, want %d", patches, tt.wantPatches)
			}
		})
	}
}
