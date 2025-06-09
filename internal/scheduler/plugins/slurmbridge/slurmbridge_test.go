// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"reflect"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	v0043 "github.com/SlinkyProject/slurm-client/api/v0043"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	tf "k8s.io/kubernetes/pkg/scheduler/testing/framework"
	"k8s.io/utils/ptr"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	_ "sigs.k8s.io/scheduler-plugins/apis/config/scheme"
)

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
	cs := clientsetfake.NewSimpleClientset()
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
		handle framework.Handle
	}
	tests := []struct {
		name    string
		args    args
		want    framework.Plugin
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
	pod := st.MakePod().Name("pod1").Labels(map[string]string{wellknown.LabelPlaceholderJobId: "1"}).Obj()
	cs := clientsetfake.NewSimpleClientset()
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
		handle        framework.Handle
	}
	type args struct {
		ctx   context.Context
		state *framework.CycleState
		pod   *corev1.Pod
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *framework.PreFilterResult
		want1  *framework.Status
	}{
		{
			name: "JobId and Node assignment exist in annotations",
			fields: fields{
				client: nil,
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0043JobInfoList{
						Items: []types.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]v0043.V0043JobInfoJobState{v0043.V0043JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return slurmcontrol.NewControl(c, "kubernetes", "slurm-bridge")
				}(),
			},
			args: args{
				ctx:   context.Background(),
				state: nil,
				pod: st.MakePod().Name("pod1").Namespace("slurm").Annotations(map[string]string{
					wellknown.AnnotationPlaceholderNode: "node1",
				}).Labels(map[string]string{
					wellknown.LabelPlaceholderJobId: "1"}).
					Obj(),
			},
			want:  &framework.PreFilterResult{NodeNames: sets.New("node1")},
			want1: framework.NewStatus(framework.Success),
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
			want1: framework.NewStatus(framework.Error, ErrorNodeConfigInvalid.Error()),
		},
		{
			name: "Creating a placeholder job fails",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, object object.Object, req any, opts ...slurmclient.CreateOption) error {
							return utilerrors.NewAggregate([]error{ErrorPodUpdateFailed})
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
			want1: framework.NewStatus(framework.Error, ErrorPodUpdateFailed.Error()),
		},
		{
			name: "Creating a placeholder job fails with invalid node resource request",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, object object.Object, req any, opts ...slurmclient.CreateOption) error {
							return utilerrors.NewAggregate([]error{ErrorNodeConfigInvalid})
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
			want1: framework.NewStatus(framework.UnschedulableAndUnresolvable, ErrorNodeConfigInvalid.Error()),
		},
		{
			name: "Create a placeholder job",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					f := interceptor.Funcs{
						Create: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.CreateOption) error {
							obj.(*types.V0043JobInfo).JobId = ptr.To(int32(1))
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
			},
			want:  nil,
			want1: framework.NewStatus(framework.Pending),
		},
		{
			name: "Placeholder job exists but nodes are not assigned",
			fields: fields{
				client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0043JobInfoList{
						Items: []types.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]v0043.V0043JobInfoJobState{v0043.V0043JobInfoJobStateRUNNING},
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
			want1: framework.NewStatus(framework.Pending, "no nodes assigned"),
		},
		{
			name: "Placeholder job exists",
			fields: fields{
				client:        kubefake.NewFakeClient(pod.DeepCopy()),
				schedulerName: "slurm-bridge-scheduler",
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0043JobInfoList{
						Items: []types.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]v0043.V0043JobInfoJobState{v0043.V0043JobInfoJobStateRUNNING},
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
			want:  &framework.PreFilterResult{NodeNames: sets.New("node1")},
			want1: framework.NewStatus(framework.Success, ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &SlurmBridge{
				Client:        tt.fields.client,
				schedulerName: tt.fields.schedulerName,
				slurmControl:  tt.fields.slurmControl,
				handle:        tt.fields.handle,
			}
			got, got1 := sb.PreFilter(tt.args.ctx, tt.args.state, tt.args.pod)
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

func TestSlurmBridge_PreFilterExtensions(t *testing.T) {
	type fields struct {
		client       kubeclient.Client
		slurmControl slurmcontrol.SlurmControlInterface
		handle       framework.Handle
	}
	tests := []struct {
		name   string
		fields fields
		want   framework.PreFilterExtensions
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
	nodeInfo := framework.NewNodeInfo()
	nodeInfo.SetNode(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}})
	podWithAnnotation := st.MakePod().Name("foo").Annotations(map[string]string{wellknown.AnnotationPlaceholderNode: "node1"}).Obj()
	podWithoutAnnotation := st.MakePod().Name("foo").Obj()
	type fields struct {
		client       kubeclient.Client
		slurmControl slurmcontrol.SlurmControlInterface
		handle       framework.Handle
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
		want   *framework.Status
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
			want: framework.NewStatus(framework.Success, ""),
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
			want: framework.NewStatus(framework.Unschedulable, "node does not match annotation"),
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

func TestSlurmBridge_deletePlaceholderJob(t *testing.T) {
	pod := st.MakePod().Name("pod1").Annotations(
		map[string]string{wellknown.AnnotationPlaceholderNode: "node1"}).Labels(
		map[string]string{wellknown.LabelPlaceholderJobId: "1"}).Obj()
	cs := clientsetfake.NewSimpleClientset()
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
		handle       framework.Handle
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
			name: "Placeholder job is deleted",
			fields: fields{
				Client: kubefake.NewFakeClient(pod.DeepCopy()),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0043JobInfoList{
						Items: []types.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
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
			}
			if err := sb.deletePlaceholderJob(tt.args.ctx, tt.args.pod); (err != nil) != tt.wantErr {
				t.Errorf("SlurmBridge.deletePlaceholderJob() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSlurmBridge_validatePodToJob(t *testing.T) {
	pod := st.MakePod().Name("pod1").Labels(map[string]string{wellknown.LabelPlaceholderJobId: "1"}).Obj()
	type fields struct {
		Client       kubeclient.Client
		slurmControl slurmcontrol.SlurmControlInterface
		handle       framework.Handle
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
				Client: kubefake.NewFakeClient(),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0043JobInfoList{
						Items: []types.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
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
			want:    pod.DeepCopy(),
			wantErr: false,
		},
		{
			name: "Matching slurm job does not exist but patch fails",
			fields: fields{
				Client: kubefake.NewFakeClient(),
				slurmControl: func() slurmcontrol.SlurmControlInterface {
					list := &types.V0043JobInfoList{
						Items: []types.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
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
					list := &types.V0043JobInfoList{
						Items: []types.V0043JobInfo{
							{V0043JobInfo: v0043.V0043JobInfo{
								AdminComment: func() *string {
									pi := placeholderinfo.PlaceholderInfo{
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
						wellknown.AnnotationPlaceholderNode: "node2",
					}
					return pod.DeepCopy()
				}(),
			},
			want: func() *corev1.Pod {
				pod.Annotations = map[string]string{
					wellknown.AnnotationPlaceholderNode: "",
				}
				pod.Labels = map[string]string{
					wellknown.LabelPlaceholderJobId: "2",
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
