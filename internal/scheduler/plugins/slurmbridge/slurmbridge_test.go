// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
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

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/testutils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func newTestSlurmControl(t *testing.T, c slurmclient.Client, mcsLabel, partition string) slurmcontrol.SlurmControlInterface {
	t.Helper()
	control, err := slurmcontrol.NewControl(c, c.GetInformer(api.ObjectTypeV0044JobInfo), mcsLabel, partition)
	if err != nil {
		t.Fatal(err)
	}
	return control
}

func slurmNode(name string, partitions ...string) api.V0044Node {
	return api.V0044Node{
		Name:       ptr.To(name),
		Partitions: ptr.To(api.V0044CsvString(partitions)),
	}
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
	podToJobsError       error
	verifyCalls          int
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

func (c *postFilterSlurmControl) GetJobsForPods(context.Context, ...*corev1.Pod) (*map[string]slurmcontrol.ExternalJob, error) {
	if c.podToJobsError != nil {
		return nil, c.podToJobsError
	}
	if c.podToJobs != nil {
		return &c.podToJobs, nil
	}
	return &map[string]slurmcontrol.ExternalJob{}, nil
}

func (c *postFilterSlurmControl) VerifyJobsForPods(_ context.Context, candidates map[string]slurmcontrol.ExternalJob, _ ...*corev1.Pod) (*map[string]slurmcontrol.ExternalJob, error) {
	c.verifyCalls++
	return &candidates, nil
}

func (c *postFilterSlurmControl) GetJob(context.Context, *corev1.Pod) (*slurmcontrol.ExternalJob, error) {
	if c.getJobCalls < len(c.getJobs) {
		job := c.getJobs[c.getJobCalls]
		c.getJobCalls++
		return job, nil
	}
	return &slurmcontrol.ExternalJob{}, nil
}

func (c *postFilterSlurmControl) ReleaseJob(context.Context, int32, *int32) error { return nil }

func (c *postFilterSlurmControl) GetAllocation(_ context.Context, _ *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]slurmcontrol.ExternalJob, error) {
	return testAllocation(ir), nil
}

func testJobComment(ir *slurmjobir.SlurmJobIR, component int) string {
	info := externaljobinfo.ExternalJobInfo{}
	for _, pod := range ir.Components[component].Pods.Items {
		info.Pods = append(info.Pods, kubeclient.ObjectKeyFromObject(&pod).String())
	}
	info.PodGroup = externaljobinfo.NewGroupIdentity(ir.Components[component].ObjectMeta)
	if ir.RootPOM.Kind == "CompositePodGroup" {
		info.CompositePodGroup = externaljobinfo.NewGroupIdentity(ir.RootPOM)
	}
	return info.ToString()
}

func testAllocation(ir *slurmjobir.SlurmJobIR) []slurmcontrol.ExternalJob {
	jobs := make([]slurmcontrol.ExternalJob, len(ir.Components))
	for i, component := range ir.Components {
		jobs[i].JobId = slurmjobir.ParseSlurmJobId(component.Pods.Items[0].Labels[wellknown.LabelExternalJobId])
	}
	return jobs
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
		fwkruntime.WithInformerFactory(informerFactory),
		fwkruntime.WithPodActivator(&activateRecorder{}))
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
			name: "pod with required node affinity is rejected",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod: st.MakePod().Name("pod1").NodeAffinity(&corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"rack-a"}},
								},
							},
						},
					},
				}).Obj(),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodWithRequiredAffinity.Error()),
		},
		{
			name: "pod with required pod anti-affinity is rejected",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				handle: f,
			},
			args: args{
				ctx:   context.Background(),
				state: framework.NewCycleState(),
				pod: st.MakePod().Name("pod1").
					PodAntiAffinity("kubernetes.io/hostname", &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "example"},
					}, st.PodAntiAffinityWithRequiredReq).
					Obj(),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.UnschedulableAndUnresolvable, ErrorPodWithRequiredAffinity.Error()),
		},
		{
			name: "pod with preferred node affinity is allowed, Slurm just won't honor the preference",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
					NodeAffinity(&corev1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
							{
								Weight: 1,
								Preference: corev1.NodeSelectorTerm{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{Key: "topology.kubernetes.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"rack-a"}},
									},
								},
							},
						},
					}).
					Obj(),
			},
			want:  &fwk.PreFilterResult{NodeNames: sets.New("node1")},
			want1: fwk.NewStatus(fwk.Success),
		},
		{
			name: "JobId and Node assignment exist in annotations",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
						WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(1)), AdminComment: ptr.To(`{"pods":["/pod1"]}`)}).
						WithInterceptorFuncs(f).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To(""),
							},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
				}(),
				handle: f,
			},
			args: args{
				ctx:   ctx,
				state: framework.NewCycleState(),
				pod:   pod.DeepCopy(),
			},
			want:  nil,
			want1: fwk.NewStatus(fwk.Unschedulable, ErrorNoKubeNodeMatch.Error()),
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
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			sb.kubeNodeIndex = testKubeNodeIndex(t, sb.Client)
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
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2},
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
		slurmControl: newTestSlurmControl(t, slurmClient, "kubernetes", "slurm-bridge"),
		draRegistry:  testutils.DRARegistryWithExampleGPU(),
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

func TestSlurmBridge_PostFilterSkipsGangShortOfQuorum(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Alpha2)

	const (
		namespace = "slurm-bridge"
		pgName    = "podgroup"
	)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName + "-a"},
		Spec: corev1.PodSpec{
			SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To(pgName)},
			Containers:      []corev1.Container{{Name: "c"}},
		},
	}
	podGroup := &slurmjobir.PodGroup{
		TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1alpha2", Kind: "PodGroup"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: pgName},
		Spec: slurmjobir.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2},
			},
		},
	}
	kubeClient := kubefake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod.DeepCopy(), podGroup.DeepCopy()).
		Build()
	sb := &SlurmBridge{
		Client:       kubeClient,
		slurmControl: newTestSlurmControl(t, fake.NewClientBuilder().Build(), "kubernetes", "slurm-bridge"),
		workloadAPI:  workloadAPI,
	}

	state := framework.NewCycleState()
	if _, status := sb.PreFilter(ctx, state, pod.DeepCopy(), nil); status.Code() != fwk.Unschedulable {
		t.Fatalf("PreFilter() status = %v, want Unschedulable", status)
	}
	if _, status := sb.PostFilter(ctx, state, pod.DeepCopy(), nil); status.Code() != fwk.Unschedulable {
		t.Fatalf("PostFilter() status = %v, want Unschedulable without submitting", status)
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
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2},
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
		list := &api.V0044JobInfoObjectList{
			Items: []api.V0044JobInfo{
				{
					AdminComment: func() *string {
						pi := externaljobinfo.ExternalJobInfo{
							PodGroup: externaljobinfo.NewGroupIdentity(metav1.PartialObjectMetadata{TypeMeta: podGroup.TypeMeta, ObjectMeta: podGroup.ObjectMeta}),
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
				},
			},
		}
		c := fake.NewClientBuilder().
			WithLists(list).
			Build()
		return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
	}()
	sb := &SlurmBridge{
		Client:        kubeClient,
		schedulerName: "slurm-bridge-scheduler",
		slurmControl:  slurmControl,
		draRegistry:   dra.DefaultRegistry(),
		workloadAPI:   workloadAPI,
	}

	rootGroupState := framework.NewCycleState()
	rootPlacementState := framework.NewCycleState()
	rootPlacementState.SetPodGroupSchedulingCycle(rootGroupState)
	groupState := framework.NewCycleState()
	groupState.SetPlacementCycleState(rootPlacementState)
	state := framework.NewCycleState()
	state.SetPodGroupSchedulingCycle(groupState)
	got, status := sb.PreFilter(ctx, state, podA.DeepCopy(), nil)
	if status.Code() != fwk.Success {
		t.Fatalf("PreFilter() status = %v, want Success: %v", status.Code(), status.Reasons())
	}
	if !apiequality.Semantic.DeepEqual(got, &fwk.PreFilterResult{NodeNames: sets.New("node1")}) {
		t.Fatalf("PreFilter() result = %v, want node1", got)
	}

	data, err := groupState.Read(nativeGroupCycleKey)
	if err != nil {
		t.Fatalf("PreFilter() did not write native group cycle state: %v", err)
	}
	group := data.(*nativeGroupCycle)
	if len(group.members) != 1 || group.members[0].state != state || group.members[0].pod.Name != podA.Name {
		t.Fatalf("PreFilter() native group members = %#v, want %s", group.members, podA.Name)
	}
	if group.members[0].result == nil || !group.members[0].status.IsSuccess() {
		t.Fatalf("PreFilter() native group member = %#v; want ready Success", group.members[0])
	}
	if _, err := rootGroupState.Read(nativeGroupCycleKey); err != nil {
		t.Fatalf("PreFilter() did not write root composite group cycle state: %v", err)
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

func TestGroupProgressRecordComponentTracksReorderedComponents(t *testing.T) {
	component := func(name string, excluded ...string) slurmjobir.SlurmJobComponent {
		return slurmjobir.SlurmJobComponent{
			ObjectMeta: metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
				Namespace: "workload",
				Name:      name,
			}},
			JobInfo: slurmjobir.SlurmJobIRJobInfo{ExcNodes: excluded},
		}
	}
	first := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{
		component("leaf-a", "node-a"),
		component("leaf-b"),
	}}
	progress := groupProgress{components: make(map[k8stypes.NamespacedName][]string)}
	if complete := progress.recordComponent(first, 0); complete {
		t.Fatal("groupProgress.recordComponent(first, 0) = true, want false")
	}

	second := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{
		component("leaf-b", "node-b"),
		component("leaf-a"),
	}}
	if complete := progress.recordComponent(second, 0); !complete {
		t.Fatal("groupProgress.recordComponent(second, 0) = false, want true")
	}
	want := map[k8stypes.NamespacedName][]string{
		{Namespace: "workload", Name: "leaf-a"}: {"node-a"},
		{Namespace: "workload", Name: "leaf-b"}: {"node-b"},
	}
	got := make(map[k8stypes.NamespacedName][]string, len(second.Components))
	for _, component := range second.Components {
		got[component.GetNamespacedName()] = component.JobInfo.ExcNodes
	}
	if !apiequality.Semantic.DeepEqual(got, want) {
		t.Errorf("recording reordered components produced exclusions %v, want %v", got, want)
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
		nodes := &api.V0044NodeObjectList{
			Items: []api.V0044Node{
				slurmNode("node1", "slurm-bridge"),
				slurmNode("node2", "slurm-bridge"),
			},
		}
		base := fake.NewClientBuilder().
			WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(1)), AdminComment: ptr.To(`{"pods":["/pod1"]}`), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}}).
			WithLists(nodes).
			Build()
		jobGets := 0
		f := interceptor.Funcs{
			Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
				job, ok := obj.(*api.V0044JobInfo)
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
				*job = api.V0044JobInfo{
					JobId:    ptr.To(int32(1)),
					JobState: &[]api.V0044JobInfoJobState{state},
					Nodes:    ptr.To(nodes),
				}
				return nil
			},
			Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
				return errors.Join(
					errors.New("Internal Server Error"),
					errors.New("Job is no longer pending execution"),
				)
			},
		}
		return newTestSlurmControl(t, interceptor.NewClient(base, f), "kubernetes", "slurm-bridge")
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
						WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(1)), AdminComment: ptr.To(`{"pods":["/pod1"]}`)}).
						WithInterceptorFuncs(f).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
					jobs := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{Pods: []string{"/pod1"}}
									return ptr.To(pi.ToString())
								}(),
							},
						},
					}
					c := fake.NewClientBuilder().WithLists(jobs).Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			want1:        fwk.NewStatus(fwk.Success, ""),
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
					return newTestSlurmControl(t, interceptor.NewClient(fake.NewClientBuilder().WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(1)), AdminComment: ptr.To(`{"pods":["/pod1"]}`), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}}).Build(), f), "kubernetes", "slurm-bridge")
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
							obj.(*api.V0044JobInfo).JobId = ptr.To(int32(1))
							return nil
						},
					}
					c := fake.NewClientBuilder().
						WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(1)), AdminComment: ptr.To(`{"pods":["/pod1"]}`), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}}).
						WithInterceptorFuncs(f).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			want1: fwk.NewStatus(fwk.Unschedulable, "insufficient eligible nodes for allocation"),
		},
		{
			name: "Updating a registered job excludes only infeasible nodes in job partition",
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
							if jobUpdate.Hold != nil {
								return nil
							}
							want := ptr.To(api.V0044CsvString{"node2"})
							if !reflect.DeepEqual(jobUpdate.ExcludedNodes, want) {
								return fmt.Errorf("ExcludedNodes = %v, want %v", jobUpdate.ExcludedNodes, want)
							}
							obj.(*api.V0044JobInfo).JobId = ptr.To(int32(1))
							return nil
						},
					}
					nodes := &api.V0044NodeObjectList{
						Items: []api.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
							slurmNode("node3", "other"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(nodes).
						WithObjects(&api.V0044JobInfo{JobId: ptr.To(int32(1)), AdminComment: ptr.To(`{"pods":["/pod1"]}`), JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}}).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			want1:        fwk.NewStatus(fwk.Success, ""),
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
							if jobUpdate.Hold != nil {
								return nil
							}
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
					jobs := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
								Nodes:    ptr.To(""),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
							},
						},
					}
					nodes := &api.V0044NodeObjectList{
						Items: []api.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(jobs, nodes).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			want1:        fwk.NewStatus(fwk.Success, ""),
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
					jobs := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
								Nodes:    ptr.To(""),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
							},
						},
					}
					nodes := &api.V0044NodeObjectList{
						Items: []api.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(jobs, nodes).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			want:  nil,
			want1: fwk.NewStatus(fwk.Unschedulable, "allocation changed while updating eligibility"),

			wantActivate: false,
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
			want1:        fwk.NewStatus(fwk.Unschedulable, "allocation changed while updating eligibility"),
			wantActivate: false,
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
					jobs := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								JobId:    ptr.To(int32(1)),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To(""),
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
							},
						},
					}
					nodes := &api.V0044NodeObjectList{
						Items: []api.V0044Node{
							slurmNode("node1", "slurm-bridge"),
							slurmNode("node2", "slurm-bridge"),
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						WithLists(jobs, nodes).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			want1:        fwk.NewStatus(fwk.Success, ""),
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
			sb.kubeNodeIndex = testKubeNodeIndex(t, sb.Client)
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
			if strings.Join(got1.Reasons(), "") != strings.Join(tt.want1.Reasons(), "") {
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
				slurmControl: newTestSlurmControl(t,
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
				slurmControl: newTestSlurmControl(t, fake.NewFakeClient(), "kubernetes", "slurm-bridge"),
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
				slurmControl: newTestSlurmControl(t, fake.NewFakeClient(), "kubernetes", "slurm-bridge"),
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
			name: "Metadata is cleared when job does not exist",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: newTestSlurmControl(t,
					fake.NewFakeClient(), "kubernetes", "slurm-bridge"),
				handle: f,
			},
			args: args{
				ctx: context.Background(),
				pod: pod.DeepCopy(),
			},
			wantErr: false,
		},
		{
			name: "External job is deleted",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								JobId: ptr.To[int32](1),
							},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return newTestSlurmControl(t, c, "kubernetes", "slurm-bridge")
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
			got := &corev1.Pod{}
			if err := sb.Get(tt.args.ctx, kubeclient.ObjectKeyFromObject(tt.args.pod), got); err != nil {
				t.Fatal(err)
			}
			if got.Labels[wellknown.LabelExternalJobId] != "" || got.Annotations[wellknown.AnnotationExternalJobNode] != "" {
				t.Fatal("allocation metadata was not cleared")
			}
		})
	}
}

func TestSlurmBridge_annotatePodsWithNodes_doesNotReshuffleAlreadyAssignedPod(t *testing.T) {
	ctx := context.Background()
	pod1 := st.MakePod().Name("pod1").UID("uid1").
		Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).
		Annotations(map[string]string{wellknown.AnnotationExternalJobNode: "node1"}).Obj()
	pod2 := st.MakePod().Name("pod2").UID("uid2").
		Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj()

	fakeClient := kubefake.NewFakeClient(pod1.DeepCopy(), pod2.DeepCopy())
	sb := &SlurmBridge{Client: fakeClient}

	// Only node2 is left in the pool -- if pod1 (already on node1) got
	// reshuffled instead of skipped, this call would either hand node1 or
	// node2 to pod2 nondeterministically and potentially collide with pod1.
	kubeNodes := sets.New("node1", "node2")
	podList := &corev1.PodList{Items: []corev1.Pod{*pod1, *pod2}}
	if err := sb.annotatePodsWithNodes(ctx, 1, kubeNodes, podList); err != nil {
		t.Fatalf("annotatePodsWithNodes() error = %v, want nil", err)
	}

	var got1, got2 corev1.Pod
	if err := fakeClient.Get(ctx, kubeclient.ObjectKeyFromObject(pod1), &got1); err != nil {
		t.Fatal(err)
	}
	if err := fakeClient.Get(ctx, kubeclient.ObjectKeyFromObject(pod2), &got2); err != nil {
		t.Fatal(err)
	}
	if got1.Annotations[wellknown.AnnotationExternalJobNode] != "node1" {
		t.Errorf("pod1 node annotation = %q, want unchanged %q", got1.Annotations[wellknown.AnnotationExternalJobNode], "node1")
	}
	if got2.Annotations[wellknown.AnnotationExternalJobNode] != "node2" {
		t.Errorf("pod2 node annotation = %q, want %q", got2.Annotations[wellknown.AnnotationExternalJobNode], "node2")
	}
}

func TestSlurmBridge_PostFilterWithoutJobIR(t *testing.T) {
	state := framework.NewCycleState()
	state.Write(stateKey, &stateData{})
	_, status := (&SlurmBridge{}).PostFilter(context.Background(), state, st.MakePod().Obj(), nil)
	if status.Code() != fwk.Unschedulable {
		t.Fatalf("status = %v, want Unschedulable", status)
	}
}

func TestSlurmBridge_annotatePodsWithNodes_skipsDeletedSibling(t *testing.T) {
	ctx := context.Background()
	// pod1 is in podList (e.g. from a snapshot taken earlier) but not in the
	// fake client, so patching it returns NotFound -- simulating a sibling
	// deleted between the snapshot and this call.
	pod1 := st.MakePod().Name("pod1").UID("uid1").
		Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj()
	pod2 := st.MakePod().Name("pod2").UID("uid2").
		Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj()

	cs := clientsetfake.NewClientset()
	informerFactory := informers.NewSharedInformerFactory(cs, 0)
	registeredPlugins := []tf.RegisterPluginFunc{
		tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
		tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
	}
	activator := &activateRecorder{}
	f, err := tf.NewFramework(
		ctx, registeredPlugins, "slurm-bridge",
		fwkruntime.WithInformerFactory(informerFactory),
		fwkruntime.WithPodActivator(activator))
	if err != nil {
		t.Fatal(err)
	}

	fakeClient := kubefake.NewFakeClient(pod2.DeepCopy())
	sb := &SlurmBridge{Client: fakeClient, handle: f}

	kubeNodes := sets.New("node1", "node2")
	podList := &corev1.PodList{Items: []corev1.Pod{*pod1, *pod2}}
	if err := sb.annotatePodsWithNodes(ctx, 1, kubeNodes, podList); err != nil {
		t.Fatalf("annotatePodsWithNodes() error = %v, want nil (deleted sibling should be skipped)", err)
	}

	var got2 corev1.Pod
	if err := fakeClient.Get(ctx, kubeclient.ObjectKeyFromObject(pod2), &got2); err != nil {
		t.Fatal(err)
	}
	if got2.Annotations[wellknown.AnnotationExternalJobNode] == "" {
		t.Error("pod2 was not assigned a node; the deleted sibling should not have starved it")
	}
}

type allocationRecoveryControl struct {
	postFilterSlurmControl
	deleteErr error
}

func (c *allocationRecoveryControl) DeleteJob(ctx context.Context, pod *corev1.Pod) error {
	_ = c.postFilterSlurmControl.DeleteJob(ctx, pod)
	return c.deleteErr
}

func TestPreFilterMissingAllocatedNode(t *testing.T) {
	lookupError := apierrors.NewServiceUnavailable("node lookup unavailable")
	deleteError := errors.New("job cancellation failed")
	patchError := errors.New("pod cleanup failed")
	for _, tt := range []struct {
		name        string
		annotation  string
		nodeExists  bool
		staleCache  bool
		slurmNode   string
		unsynced    bool
		getErr      error
		listErr     error
		deleteErr   error
		patchErr    error
		wantCode    fwk.Code
		wantDeleted int
		wantCleared bool
	}{
		{name: "missing node before assignment", wantCode: fwk.Unschedulable, wantDeleted: 1, wantCleared: true},
		{name: "missing previously assigned node", annotation: "node1", wantCode: fwk.Unschedulable, wantDeleted: 1, wantCleared: true},
		{name: "valid allocation", nodeExists: true, wantCode: fwk.Success},
		{name: "valid previously assigned node", nodeExists: true, annotation: "node1", wantCode: fwk.Success},
		{name: "cached miss with live node", nodeExists: true, staleCache: true, wantCode: fwk.Error},
		{name: "cached miss with live Slurm alias", nodeExists: true, staleCache: true, slurmNode: "slurm-node1", wantCode: fwk.Error},
		{name: "cached miss with failed live list", staleCache: true, listErr: lookupError, wantCode: fwk.Error},
		{name: "node informer not synced", unsynced: true, wantCode: fwk.Error},
		{name: "assigned node lookup unavailable", annotation: "node1", getErr: lookupError, wantCode: fwk.Error},
		{name: "cancellation fails", deleteErr: deleteError, wantCode: fwk.Error, wantDeleted: 1},
		{name: "metadata cleanup fails", annotation: "node1", patchErr: patchError, wantCode: fwk.Error, wantDeleted: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "pod1", Namespace: "default", UID: "pod1",
				Labels:      map[string]string{wellknown.LabelExternalJobId: "1", "keep": "label"},
				Annotations: map[string]string{"keep": "annotation"},
			}}
			if tt.annotation != "" {
				pod.Annotations[wellknown.AnnotationExternalJobNode] = tt.annotation
			}
			objects := []kubeclient.Object{pod.DeepCopy()}
			slurmNode := tt.slurmNode
			if slurmNode == "" {
				slurmNode = "node1"
			}
			if tt.nodeExists {
				objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{
					Name: "node1", Labels: map[string]string{wellknown.LabelSlurmNodeName: slurmNode},
				}})
			}
			base := kubefake.NewClientBuilder().WithObjects(objects...).Build()
			listCalls, patchCalls := 0, 0
			kc := kubeinterceptor.NewClient(base, kubeinterceptor.Funcs{
				Get: func(ctx context.Context, _ kubeclient.WithWatch, key kubeclient.ObjectKey, obj kubeclient.Object, opts ...kubeclient.GetOption) error {
					if _, ok := obj.(*corev1.Node); ok && tt.getErr != nil {
						return tt.getErr
					}
					return base.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, _ kubeclient.WithWatch, list kubeclient.ObjectList, opts ...kubeclient.ListOption) error {
					if _, ok := list.(*corev1.NodeList); ok {
						listCalls++
						if tt.listErr != nil {
							return tt.listErr
						}
					}
					return base.List(ctx, list, opts...)
				},
				Patch: func(ctx context.Context, _ kubeclient.WithWatch, obj kubeclient.Object, patch kubeclient.Patch, opts ...kubeclient.PatchOption) error {
					patchCalls++
					if tt.patchErr != nil {
						return tt.patchErr
					}
					return base.Patch(ctx, obj, patch, opts...)
				},
			})
			control := &allocationRecoveryControl{postFilterSlurmControl: postFilterSlurmControl{
				getJobs: []*slurmcontrol.ExternalJob{{JobId: 1, Nodes: slurmNode}},
			}, deleteErr: tt.deleteErr}
			activator := &activateRecorder{}
			handle, err := fwkruntime.NewFramework(ctx, nil, nil,
				fwkruntime.WithClientSet(clientsetfake.NewClientset()), fwkruntime.WithPodActivator(activator))
			if err != nil {
				t.Fatal(err)
			}
			indexClient := base
			if tt.staleCache {
				indexClient = kubefake.NewClientBuilder().Build()
			}
			sb := &SlurmBridge{Client: kc, kubeNodeIndex: testKubeNodeIndex(t, indexClient), slurmControl: control, handle: handle}
			sb.kubeNodeIndex.hasSynced = func() bool { return !tt.unsynced }
			state := framework.NewCycleState()
			result, status := sb.PreFilter(ctx, state, pod.DeepCopy(), nil)
			if status.Code() != tt.wantCode {
				t.Fatalf("PreFilter() = %v, want %v", status, tt.wantCode)
			}
			if tt.wantCode == fwk.Success && (result == nil || !result.NodeNames.Equal(sets.New("node1"))) {
				t.Fatalf("result = %v, want node1", result)
			}
			if control.deleteCalls != tt.wantDeleted {
				t.Fatalf("DeleteJob calls = %d, want %d", control.deleteCalls, tt.wantDeleted)
			}
			got := &corev1.Pod{}
			if err := base.Get(ctx, kubeclient.ObjectKeyFromObject(pod), got); err != nil {
				t.Fatal(err)
			}
			if tt.staleCache {
				if listCalls != 1 || patchCalls != 0 {
					t.Fatalf("live node lists = %d, patches = %d, want 1, 0", listCalls, patchCalls)
				}
				if !reflect.DeepEqual(got.Labels, pod.Labels) || !reflect.DeepEqual(got.Annotations, pod.Annotations) {
					t.Fatal("allocation metadata changed after an unconfirmed cache miss")
				}
				if tt.listErr != nil && status.Message() != tt.listErr.Error() {
					t.Fatalf("PreFilter() = %v, want live list error %v", status, tt.listErr)
				}
			}
			if cleared := got.Labels[wellknown.LabelExternalJobId] == ""; cleared != tt.wantCleared {
				t.Fatalf("allocation cleared = %v, want %v", cleared, tt.wantCleared)
			}
			if got.Labels["keep"] != "label" || got.Annotations["keep"] != "annotation" {
				t.Fatal("unrelated metadata changed")
			}
			if tt.wantCleared {
				if got.Annotations[wellknown.AnnotationExternalJobNode] != "" {
					t.Fatal("node annotation not cleared")
				}
				if activated := activator.pods[string(pod.UID)]; activated == nil || activated.Labels[wellknown.LabelExternalJobId] != "" || activated.Annotations[wellknown.AnnotationExternalJobNode] != "" {
					t.Fatalf("activated pod = %v, want cleared allocation", activated)
				}
				// The same scheduling cycle has no feasibility results to submit.
				getCalls := control.getJobCalls
				_, postStatus := sb.PostFilter(ctx, state, pod, nil)
				if postStatus.Code() != fwk.Unschedulable || control.getJobCalls != getCalls {
					t.Fatalf("PostFilter resumed invalidated cycle: %v", postStatus)
				}
			} else if len(activator.pods) != 0 {
				t.Fatal("pod activated without successful cleanup")
			}
		})
	}
}

func TestMissingAllocatedNodeRetriesMetadataCleanup(t *testing.T) {
	for _, canceledJobRetained := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled job retained=%v", canceledJobRetained), func(t *testing.T) {
			ctx := t.Context()
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "pod1", Namespace: "default", UID: "pod1",
				Labels:      map[string]string{wellknown.LabelExternalJobId: "1", "keep": "label"},
				Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node1", "keep": "annotation"},
				Finalizers:  []string{wellknown.FinalizerScheduler},
			}}
			base := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy()).Build()
			patchErr := errors.New("temporary cleanup failure")
			patchCalls := 0
			kc := kubeinterceptor.NewClient(base, kubeinterceptor.Funcs{
				Patch: func(ctx context.Context, _ kubeclient.WithWatch, obj kubeclient.Object, patch kubeclient.Patch, opts ...kubeclient.PatchOption) error {
					patchCalls++
					if patchCalls == 1 {
						return patchErr
					}
					return base.Patch(ctx, obj, patch, opts...)
				},
			})
			info := externaljobinfo.ExternalJobInfo{Pods: []string{externaljobinfo.PodKey(pod)}}
			job := &api.V0044JobInfo{
				JobId: ptr.To(int32(1)), Nodes: ptr.To("node1"), AdminComment: ptr.To(info.ToString()),
				JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
			}
			slurmBase := fake.NewClientBuilder().WithObjects(job).Build()
			deleteCalls := 0
			slurmClient := interceptor.NewClient(slurmBase, interceptor.Funcs{
				Delete: func(ctx context.Context, obj object.Object, opts ...slurmclient.DeleteOption) error {
					deleteCalls++
					if !canceledJobRetained {
						return slurmBase.Delete(ctx, obj, opts...)
					}
					if deleteCalls > 1 {
						return errors.New("job already completed")
					}
					job.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED}
					return slurmBase.Update(ctx, job, nil)
				},
			})
			activator := &activateRecorder{}
			handle, err := fwkruntime.NewFramework(ctx, nil, nil,
				fwkruntime.WithClientSet(clientsetfake.NewClientset()), fwkruntime.WithPodActivator(activator))
			if err != nil {
				t.Fatal(err)
			}
			sb := &SlurmBridge{Client: kc, kubeNodeIndex: testKubeNodeIndex(t, kc), slurmControl: newTestSlurmControl(t, slurmClient, "kubernetes", "slurm-bridge"), handle: handle}
			_, status := sb.PreFilter(ctx, framework.NewCycleState(), pod.DeepCopy(), nil)
			if status.Code() != fwk.Error || status.Message() != patchErr.Error() {
				t.Fatalf("first PreFilter() = %v, want cleanup error", status)
			}
			if len(activator.pods) != 0 {
				t.Fatal("pod activated before cleanup completed")
			}
			fresh := &corev1.Pod{}
			if err := base.Get(ctx, kubeclient.ObjectKeyFromObject(pod), fresh); err != nil {
				t.Fatal(err)
			}
			_, status = sb.PreFilter(ctx, framework.NewCycleState(), fresh, nil)
			if status.Code() != fwk.Unschedulable || deleteCalls != 2 || patchCalls != 2 {
				t.Fatalf("retry PreFilter() = %v, cancellations = %d, patches = %d", status, deleteCalls, patchCalls)
			}
			if err := base.Get(ctx, kubeclient.ObjectKeyFromObject(pod), fresh); err != nil {
				t.Fatal(err)
			}
			if fresh.Labels[wellknown.LabelExternalJobId] != "" || fresh.Annotations[wellknown.AnnotationExternalJobNode] != "" {
				t.Fatal("retry did not clear allocation metadata")
			}
			if fresh.Labels["keep"] != "label" || fresh.Annotations["keep"] != "annotation" {
				t.Fatal("unrelated metadata changed")
			}
			if activated := activator.pods[string(pod.UID)]; activated == nil || activated.Labels[wellknown.LabelExternalJobId] != "" || activated.Annotations[wellknown.AnnotationExternalJobNode] != "" {
				t.Fatalf("activated pod = %v, want cleared allocation", activated)
			}
			result, status := sb.PreFilter(ctx, framework.NewCycleState(), fresh, nil)
			if status.Code() != fwk.Unschedulable || result != nil {
				t.Fatalf("fresh scheduling cycle = %v, %v, want wait for controller registration", result, status)
			}
		})
	}
}

func TestMissingAllocatedNodeRecomputesFeasibleNodes(t *testing.T) {
	ctx := t.Context()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default", UID: "pod1", Labels: map[string]string{wellknown.LabelExternalJobId: "1"}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}
	kc := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy(), node).Build()
	control := &postFilterSlurmControl{
		getJobs:              []*slurmcontrol.ExternalJob{{JobId: 1, Nodes: "node1"}, {}, {}},
		nodeNamesByPartition: map[string][]string{"": {"node1", "node2"}},
	}
	handle, err := fwkruntime.NewFramework(ctx, nil, nil,
		fwkruntime.WithClientSet(clientsetfake.NewClientset()),
		fwkruntime.WithPodActivator(&activateRecorder{}),
		fwkruntime.WithSnapshotSharedLister(internalcache.NewSnapshot(nil, []*corev1.Node{node})))
	if err != nil {
		t.Fatal(err)
	}
	sb := &SlurmBridge{Client: kc, kubeNodeIndex: testKubeNodeIndex(t, kc), slurmControl: control, handle: handle}
	_, status := sb.PreFilter(ctx, framework.NewCycleState(), pod.DeepCopy(), nil)
	if status.Code() != fwk.Unschedulable {
		t.Fatalf("first PreFilter() = %v", status)
	}
	fresh := &corev1.Pod{}
	if err := kc.Get(ctx, kubeclient.ObjectKeyFromObject(pod), fresh); err != nil {
		t.Fatal(err)
	}
	// Registration publishes a replacement held allocation before scheduling resumes.
	fresh.Labels = map[string]string{wellknown.LabelExternalJobId: "2"}
	if err := kc.Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	control.getJobs = []*slurmcontrol.ExternalJob{{JobId: 2, Pending: true}, {JobId: 2, Pending: true}}
	control.getJobCalls = 0
	var updatedIR *slurmjobir.SlurmJobIR
	control.updateJob = func(ir *slurmjobir.SlurmJobIR) (int32, error) {
		updatedIR = ir
		return 2, nil
	}
	state := framework.NewCycleState()
	result, status := sb.PreFilter(ctx, state, fresh, nil)
	if !status.IsSuccess() || result != nil {
		t.Fatalf("retry PreFilter() = %v, %v", result, status)
	}
	_, status = sb.PostFilter(ctx, state, fresh, framework.NewNodeToStatus(map[string]*fwk.Status{
		"node2": fwk.NewStatus(fwk.Unschedulable).WithPlugin(Name),
	}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable)))
	if !status.IsSuccess() {
		t.Fatalf("retry PostFilter() = %v", status)
	}
	if updatedIR == nil {
		t.Fatal("replacement allocation eligibility was not updated")
	}
	if nodes := updatedIR.Components[0].JobInfo.ExcNodes; !sets.New(nodes...).Equal(sets.New("node1")) {
		t.Fatalf("replacement excluded nodes = %v, want node1", nodes)
	}
}

func TestMissingAllocatedNodeClearsMatchingGangPods(t *testing.T) {
	ctx := t.Context()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workloadAPI := mustRegisterTestWorkloadAPI(t, scheme, slurmjobir.WorkloadAPIVersionV1Alpha2)
	pg := &slurmjobir.PodGroup{
		TypeMeta:   workloadAPI.PodGroupTypeMeta,
		ObjectMeta: metav1.ObjectMeta{Name: "gang", Namespace: "default"},
		Spec: slurmjobir.PodGroupSpec{SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
			Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 2},
		}},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default", UID: "pod1", Labels: map[string]string{wellknown.LabelExternalJobId: "1"}},
		Spec: corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("gang")}},
	}
	sibling := pod.DeepCopy()
	sibling.Name = "pod2"
	sibling.UID = "pod2"
	sibling.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node1"}
	otherJobPod := pod.DeepCopy()
	otherJobPod.Name = "pod3"
	otherJobPod.UID = "pod3"
	otherJobPod.Labels[wellknown.LabelExternalJobId] = "2"
	otherJobPod.Annotations = map[string]string{wellknown.AnnotationExternalJobNode: "node3"}
	kc := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(pg, pod.DeepCopy(), sibling, otherJobPod,
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}).Build()
	control := &postFilterSlurmControl{getJobs: []*slurmcontrol.ExternalJob{{JobId: 1, Nodes: "node[1-2]"}}}
	handle, err := fwkruntime.NewFramework(ctx, nil, nil, fwkruntime.WithClientSet(clientsetfake.NewClientset()), fwkruntime.WithPodActivator(&activateRecorder{}))
	if err != nil {
		t.Fatal(err)
	}
	sb := &SlurmBridge{Client: kc, kubeNodeIndex: testKubeNodeIndex(t, kc), slurmControl: control, handle: handle, workloadAPI: workloadAPI}
	status := sb.handleAllocatedNodeError(ctx, &stateData{}, pod.DeepCopy(), ErrorNoKubeNodeMatch)
	if status.Code() != fwk.Unschedulable || control.deleteCalls != 1 {
		t.Fatalf("handleAllocatedNodeError() = %v, cancellations = %d", status, control.deleteCalls)
	}
	for _, p := range []*corev1.Pod{pod, sibling, otherJobPod} {
		got := &corev1.Pod{}
		if err := kc.Get(ctx, kubeclient.ObjectKeyFromObject(p), got); err != nil {
			t.Fatal(err)
		}
		if p.Name == otherJobPod.Name {
			if got.Labels[wellknown.LabelExternalJobId] != "2" || got.Annotations[wellknown.AnnotationExternalJobNode] != "node3" {
				t.Fatal("different job allocation changed")
			}
		} else if got.Labels[wellknown.LabelExternalJobId] != "" || got.Annotations[wellknown.AnnotationExternalJobNode] != "" {
			t.Fatalf("gang pod %s allocation was not cleared", p.Name)
		}
	}
}

func TestUpdateRaceWithMissingAllocatedNode(t *testing.T) {
	ctx := t.Context()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod1", Namespace: "default", UID: "pod1", Labels: map[string]string{wellknown.LabelExternalJobId: "1"}}}
	kc := kubefake.NewClientBuilder().WithObjects(pod.DeepCopy()).Build()
	control := &postFilterSlurmControl{
		getJobs: []*slurmcontrol.ExternalJob{{JobId: 1, Nodes: "node1"}},
		updateJob: func(*slurmjobir.SlurmJobIR) (int32, error) {
			return 0, errors.New("Job is no longer pending execution")
		},
	}
	handle, err := fwkruntime.NewFramework(ctx, nil, nil, fwkruntime.WithClientSet(clientsetfake.NewClientset()), fwkruntime.WithPodActivator(&activateRecorder{}))
	if err != nil {
		t.Fatal(err)
	}
	sb := &SlurmBridge{Client: kc, kubeNodeIndex: testKubeNodeIndex(t, kc), slurmControl: control, handle: handle}
	ir, err := slurmjobir.TranslateToSlurmJobIR(kc, sb.registry(), nil, ctx, pod)
	if err != nil {
		t.Fatal(err)
	}
	s := &stateData{slurmJobIR: ir}
	status := sb.updateExternalJob(ctx, s, false)
	if status.Code() != fwk.Unschedulable || control.deleteCalls != 0 {
		t.Fatalf("updateExternalJob() = %v, cancellations = %d", status, control.deleteCalls)
	}
	got := &corev1.Pod{}
	if err := kc.Get(ctx, kubeclient.ObjectKeyFromObject(pod), got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[wellknown.LabelExternalJobId] != "1" {
		t.Fatal("update race changed allocation metadata before the next scheduling cycle")
	}
	_, status = sb.PreFilter(ctx, framework.NewCycleState(), got, nil)
	if status.Code() != fwk.Unschedulable || control.deleteCalls != 1 {
		t.Fatalf("next PreFilter() = %v, cancellations = %d", status, control.deleteCalls)
	}
	if err := kc.Get(ctx, kubeclient.ObjectKeyFromObject(pod), got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[wellknown.LabelExternalJobId] != "" {
		t.Fatal("next scheduling cycle did not clear the invalid allocation")
	}
}
