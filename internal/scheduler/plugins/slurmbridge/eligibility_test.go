// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/interpodaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeports"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	tf "k8s.io/kubernetes/pkg/scheduler/testing/framework"
	"k8s.io/utils/ptr"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// Exercise submission with real Kubernetes resource and placement filters, so
// a busy node fails NodeResourcesFit before reaching SlurmBridge.Filter.
func TestPostFilterQueuesBusyNodes(t *testing.T) {
	metrics.Register()
	const schedulerName = "slurm-bridge"
	newPod := func(name, node string, requests corev1.ResourceList) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: ktypes.UID(name)},
			Spec: corev1.PodSpec{
				SchedulerName: schedulerName,
				NodeName:      node,
				Containers:    []corev1.Container{{Name: "work", Resources: corev1.ResourceRequirements{Requests: requests}}},
			},
		}
	}
	resources := func(cpu, memory, gpu string) corev1.ResourceList {
		return corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
			"nvidia.com/gpu":      resource.MustParse(gpu),
		}
	}
	affinityPeer := newPod("service", "node0", resources("1", "1Gi", "0"))
	affinityPeer.Labels = map[string]string{"app": "service"}
	tests := []struct {
		name              string
		nodes, busy, gang int
		occupied          corev1.ResourceList
		mutate            func(*corev1.Node, *corev1.Pod, *corev1.Pod)
		extraPod          *corev1.Pod
		wantSubmit        bool
		wantExcluded      []string
	}{
		{name: "48-node gang with only 16 free nodes", nodes: 64, busy: 48, gang: 48, wantSubmit: true},
		{name: "single pod with no free nodes", nodes: 1, busy: 1, gang: 1, wantSubmit: true},
		{name: "GPU occupied by Bridge job", wantSubmit: true},
		{name: "CPU occupied by Bridge job", occupied: resources("96", "1Gi", "0"), wantSubmit: true},
		{name: "memory occupied by Bridge job", occupied: resources("1", "1024Gi", "0"), wantSubmit: true},
		{name: "pod slots occupied by Bridge job", mutate: func(n *corev1.Node, _, _ *corev1.Pod) {
			n.Status.Allocatable[corev1.ResourcePods] = resource.MustParse("1")
		}, wantSubmit: true},
		{name: "GPU request exceeds node capacity", mutate: func(_ *corev1.Node, _, p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("9")
		}},
		{name: "CPU request exceeds node capacity", mutate: func(_ *corev1.Node, _, p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("97")
		}},
		{name: "memory request exceeds node capacity", mutate: func(_ *corev1.Node, _, p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("1025Gi")
		}},
		{name: "unmanaged occupant", mutate: func(_ *corev1.Node, p, _ *corev1.Pod) {
			p.Spec.SchedulerName = "default-scheduler"
			p.Labels = nil
		}},
		{name: "another scheduler with a job label", mutate: func(_ *corev1.Node, p, _ *corev1.Pod) {
			p.Spec.SchedulerName = "other-scheduler"
		}},
		{name: "Bridge occupant without a job label", mutate: func(_ *corev1.Node, p, _ *corev1.Pod) {
			p.Labels = nil
		}},
		{name: "Bridge occupant with an invalid job label", mutate: func(_ *corev1.Node, p, _ *corev1.Pod) {
			p.Labels[wellknown.LabelExternalJobId] = "invalid"
		}},
		{name: "Bridge occupant with a zero job ID", mutate: func(_ *corev1.Node, p, _ *corev1.Pod) {
			p.Labels[wellknown.LabelExternalJobId] = "0"
		}},
		{name: "unmanaged resources still prevent fit", extraPod: newPod("daemon", "node0", resources("96", "1Gi", "0"))},
		{name: "unmanaged resources leave enough capacity", extraPod: newPod("daemon", "node0", resources("1", "1Gi", "0")), wantSubmit: true},
		{name: "queue on remaining nodes while excluding unmanaged usage", nodes: 3, busy: 2, gang: 2,
			extraPod: newPod("daemon", "node0", resources("96", "1Gi", "0")), wantSubmit: true, wantExcluded: []string{"node0"}},
		{name: "resource failure conceals taint", mutate: func(n *corev1.Node, _, _ *corev1.Pod) {
			n.Spec.Taints = []corev1.Taint{{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule}}
		}},
		{name: "queue on remaining nodes while excluding a tainted node", nodes: 3, busy: 2, gang: 2,
			mutate: func(n *corev1.Node, _, _ *corev1.Pod) {
				if n.Name == "node0" {
					n.Spec.Taints = []corev1.Taint{{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule}}
				}
			}, wantSubmit: true, wantExcluded: []string{"node0"}},
		{name: "queue on remaining nodes while respecting node selector", nodes: 3, busy: 2, gang: 2,
			mutate: func(n *corev1.Node, _, p *corev1.Pod) {
				p.Spec.NodeSelector = map[string]string{"pool": "batch"}
				if n.Name == "node0" {
					n.Labels["pool"] = "other"
				}
			}, wantSubmit: true, wantExcluded: []string{"node0"}},
		{name: "removing occupant must not conceal host port conflict", mutate: func(_ *corev1.Node, occupant, pending *corev1.Pod) {
			port := []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}}
			occupant.Spec.Containers[0].Ports = port
			pending.Spec.Containers[0].Ports = port
		}},
		{name: "removing occupant must not conceal pod anti-affinity", mutate: func(_ *corev1.Node, _, pending *corev1.Pod) {
			pending.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{wellknown.LabelExternalJobId: "1"}},
					TopologyKey:   corev1.LabelHostname,
				}},
			}}
		}},
		{name: "retain affinity to an unmanaged pod", nodes: 1, busy: 1, gang: 1,
			extraPod: affinityPeer, wantSubmit: true,
			mutate: func(_ *corev1.Node, _, pending *corev1.Pod) {
				pending.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
						LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "service"}},
						TopologyKey:   corev1.LabelHostname,
					}},
				}}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			nodeCount, busyCount, gangSize := tt.nodes, tt.busy, tt.gang
			if nodeCount == 0 {
				nodeCount, busyCount, gangSize = 2, 1, 2
			}
			pending := newPod("pending0", "", resources("1", "1Gi", "8"))
			var nodes []*corev1.Node
			var occupants []*corev1.Pod
			slurmNodes := &types.V0044NodeList{}
			for i := range nodeCount {
				name := fmt.Sprintf("node%d", i)
				allocatable := resources("96", "1024Gi", "8")
				allocatable[corev1.ResourcePods] = resource.MustParse("110")
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelHostname: name, "pool": "batch"}},
					Status:     corev1.NodeStatus{Allocatable: allocatable},
				}
				if i < busyCount {
					occupied := tt.occupied
					if occupied == nil {
						occupied = resources("1", "1Gi", "8")
					}
					p := newPod("running"+strconv.Itoa(i), name, occupied)
					p.Labels = map[string]string{wellknown.LabelExternalJobId: "1"}
					if tt.mutate != nil {
						tt.mutate(node, p, pending)
					}
					occupants = append(occupants, p)
				}
				nodes = append(nodes, node)
				slurmNodes.Items = append(slurmNodes.Items, slurmNode(name, schedulerName))
			}
			if tt.extraPod != nil {
				occupants = append(occupants, tt.extraPod.DeepCopy())
			}
			// Slurm also knows a node missing from Kubernetes, which must remain
			// excluded even when busy Kubernetes nodes become eligible.
			slurmNodes.Items = append(slurmNodes.Items, slurmNode("slurm-only", schedulerName))
			slurmNodes.Items = append(slurmNodes.Items, slurmNode("other-partition", "other"))
			snapshot := internalcache.NewSnapshot(occupants, nodes)
			f, err := tf.NewFramework(ctx, []tf.RegisterPluginFunc{
				tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
				tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
				tf.RegisterPluginAsExtensions(noderesources.Name, fwkruntime.FactoryAdapter(feature.Features{}, noderesources.NewFit), "PreFilter", "Filter"),
				// Deliberately put other constraints after resources: Filter stops
				// at the first failure, so submission must check these too.
				tf.RegisterFilterPlugin(tainttoleration.Name, fwkruntime.FactoryAdapter(feature.Features{}, tainttoleration.New)),
				tf.RegisterPluginAsExtensions(nodeports.Name, fwkruntime.FactoryAdapter(feature.Features{}, nodeports.New), "PreFilter", "Filter"),
				tf.RegisterPluginAsExtensions(nodeaffinity.Name, fwkruntime.FactoryAdapter(feature.Features{}, nodeaffinity.New), "PreFilter", "Filter"),
				tf.RegisterPluginAsExtensions(interpodaffinity.Name, fwkruntime.FactoryAdapter(feature.Features{}, interpodaffinity.New), "PreFilter", "Filter"),
				tf.RegisterFilterPlugin(Name, func(context.Context, runtime.Object, fwk.Handle) (fwk.Plugin, error) { return &SlurmBridge{}, nil }),
			}, schedulerName,
				fwkruntime.WithInformerFactory(informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0)),
				fwkruntime.WithSnapshotSharedLister(snapshot),
				fwkruntime.WithPodActivator(&activateRecorder{}),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			state := framework.NewCycleState()
			if _, status, _ := f.RunPreFilterPlugins(ctx, state, pending); !status.IsSuccess() {
				t.Fatalf("PreFilter: %v", status)
			}
			statuses := make(map[string]*fwk.Status)
			before := make(map[string]fwk.NodeInfo)
			for _, node := range nodes {
				info, err := snapshot.NodeInfos().Get(node.Name)
				if err != nil {
					t.Fatal(err)
				}
				statuses[node.Name] = f.RunFilterPlugins(ctx, state, pending, info)
				before[node.Name] = info.Snapshot()
			}
			if statuses["node0"].Plugin() != noderesources.Name {
				t.Fatalf("expected original resource rejection, got %v", statuses["node0"])
			}
			m := framework.NewNodeToStatus(statuses, fwk.NewStatus(fwk.UnschedulableAndUnresolvable))
			ir := &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{}}}
			var objects []kubeclient.Object
			for i := range gangSize {
				p := pending.DeepCopy()
				p.Name = fmt.Sprintf("pending%d", i)
				p.UID = ktypes.UID(p.Name)
				ir.Components[0].Pods.Items = append(ir.Components[0].Pods.Items, *p)
				objects = append(objects, p)
			}
			state.Write(stateKey, &stateData{slurmJobIR: ir})
			creates, updates := 0, 0
			checkRequest := func(job *api.V0044JobDescMsg) {
				t.Helper()
				if got := ptr.Deref(job.Nodes, ""); got != strconv.Itoa(gangSize) {
					t.Errorf("Slurm node request = %q, want %d", got, gangSize)
				}
				wantExcluded := append(api.V0044CsvString{"slurm-only"}, tt.wantExcluded...)
				slices.Sort(wantExcluded)
				if !reflect.DeepEqual(job.ExcludedNodes, &wantExcluded) {
					t.Errorf("excluded nodes = %v, want %v", job.ExcludedNodes, wantExcluded)
				}
			}
			jobs := &types.V0044JobInfoList{Items: []types.V0044JobInfo{{V0044JobInfo: api.V0044JobInfo{
				JobId: ptr.To(int32(42)), Nodes: ptr.To(""),
				JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
			}}}}
			slurm := fake.NewClientBuilder().WithLists(slurmNodes, jobs).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, obj object.Object, req any, _ ...slurmclient.CreateOption) error {
					creates++
					checkRequest(req.(api.V0044JobSubmitReq).Job)
					obj.(*types.V0044JobInfo).JobId = ptr.To(int32(42))
					return nil
				},
				Update: func(_ context.Context, _ object.Object, req any, _ ...slurmclient.UpdateOption) error {
					updates++
					job := req.(api.V0044JobDescMsg)
					checkRequest(&job)
					return nil
				},
			}).Build()
			sb := &SlurmBridge{
				Client:        kubefake.NewClientBuilder().WithObjects(objects...).Build(),
				schedulerName: schedulerName,
				slurmControl:  slurmcontrol.NewControl(slurm, "kubernetes", schedulerName),
				handle:        f,
			}
			if _, status := sb.PostFilter(ctx, state, pending, m); !status.IsSuccess() {
				t.Fatalf("PostFilter: %v", status)
			}
			wantCreates := 0
			if tt.wantSubmit {
				wantCreates = 1
			}
			if creates != wantCreates {
				t.Fatalf("Slurm submissions = %d, want %d", creates, wantCreates)
			}
			for _, p := range ir.Components[0].Pods.Items {
				got := &corev1.Pod{}
				if err := sb.Get(ctx, kubeclient.ObjectKeyFromObject(&p), got); err != nil {
					t.Fatal(err)
				}
				wantID := ""
				if tt.wantSubmit {
					wantID = "42"
				}
				if got.Labels[wellknown.LabelExternalJobId] != wantID || got.Spec.NodeName != "" || got.Annotations[wellknown.AnnotationExternalJobNode] != "" {
					t.Errorf("pod %s: want job ID %q and no binding/allocation, got labels=%v, annotations=%v, node=%q", got.Name, wantID, got.Labels, got.Annotations, got.Spec.NodeName)
				}
			}
			if tt.wantSubmit {
				if err := sb.Get(ctx, kubeclient.ObjectKeyFromObject(pending), pending); err != nil {
					t.Fatal(err)
				}
				if _, status := sb.PostFilter(ctx, state, pending, m); !status.IsSuccess() {
					t.Fatalf("pending update: %v", status)
				}
				if creates != 1 || updates != 1 {
					t.Errorf("want one submission and one pending update, got %d/%d", creates, updates)
				}
			}
			for _, node := range nodes {
				info, _ := snapshot.NodeInfos().Get(node.Name)
				if !reflect.DeepEqual(info.Snapshot(), before[node.Name]) {
					t.Errorf("scheduler snapshot changed for %s", node.Name)
				}
				if got := f.RunFilterPlugins(ctx, state, pending, info); !reflect.DeepEqual(got, statuses[node.Name]) {
					t.Errorf("normal Filter changed for %s: got %v, want %v", node.Name, got, statuses[node.Name])
				}
			}
		})
	}
}

// Slurm may grant an allocation before Kubernetes observes the preceding job's
// pods leaving. A resource-only handoff must not cancel a partially bound gang.
func TestPostFilterAllocatedGangHandoff(t *testing.T) {
	metrics.Register()
	const schedulerName = "slurm-bridge"
	tests := []struct {
		name          string
		mutate        func(*corev1.Node, *corev1.Pod, *corev1.Pod)
		jobState      api.V0044JobInfoJobState
		missingJob    bool
		missingPods   bool
		jobNamespace  string
		occupantCount int
		wantDelete    bool
	}{
		{name: "wait for cleanup of a completed Bridge allocation"},
		{name: "many completed allocations use the same job list", occupantCount: 10},
		{name: "live allocation is still a rejection", jobState: api.V0044JobInfoJobStateRUNNING, wantDelete: true},
		{name: "pending allocation is still a rejection", jobState: api.V0044JobInfoJobStatePENDING, wantDelete: true},
		{name: "unknown job state is still a rejection", jobState: "UNKNOWN", wantDelete: true},
		{name: "missing job is still a rejection", missingJob: true, wantDelete: true},
		{name: "missing pod mapping is still a rejection", missingPods: true, wantDelete: true},
		{name: "same pod name in another namespace is still a rejection", jobNamespace: "other", wantDelete: true},
		{name: "finished job with a mismatched ID is still a rejection", mutate: func(_ *corev1.Node, occupant, _ *corev1.Pod) {
			occupant.Labels[wellknown.LabelExternalJobId] = "2"
		}, wantDelete: true},
		{name: "finished heterogeneous leader does not release a live component", mutate: func(_ *corev1.Node, occupant, _ *corev1.Pod) {
			occupant.Labels[wellknown.LabelExternalJobId] = "2"
			occupant.Labels[wellknown.LabelExternalHetJobId] = "1"
		}, wantDelete: true},
		{name: "canceled allocation can release resources", jobState: api.V0044JobInfoJobStateCANCELLED},
		{name: "terminating pod can release resources even with a live job", jobState: api.V0044JobInfoJobStateRUNNING, mutate: func(_ *corev1.Node, occupant, _ *corev1.Pod) {
			occupant.DeletionTimestamp = ptr.To(metav1.Now())
		}},
		{name: "succeeded pod can release resources even with a live job", jobState: api.V0044JobInfoJobStateRUNNING, mutate: func(_ *corev1.Node, occupant, _ *corev1.Pod) {
			occupant.Status.Phase = corev1.PodSucceeded
		}},
		{name: "failed pod can release resources even with a live job", jobState: api.V0044JobInfoJobStateRUNNING, mutate: func(_ *corev1.Node, occupant, _ *corev1.Pod) {
			occupant.Status.Phase = corev1.PodFailed
		}},
		{name: "unmanaged usage is still a rejection", mutate: func(_ *corev1.Node, occupant, _ *corev1.Pod) {
			occupant.Spec.SchedulerName = "default-scheduler"
		}, wantDelete: true},
		{name: "same job occupancy is not a handoff", mutate: func(_ *corev1.Node, occupant, _ *corev1.Pod) {
			occupant.Labels[wellknown.LabelExternalJobId] = "42"
		}, wantDelete: true},
		{name: "same heterogeneous allocation is not a handoff", mutate: func(_ *corev1.Node, occupant, pending *corev1.Pod) {
			occupant.Labels[wellknown.LabelExternalHetJobId] = "40"
			pending.Labels[wellknown.LabelExternalHetJobId] = "40"
		}, wantDelete: true},
		{name: "resource failure must not conceal a taint", mutate: func(node *corev1.Node, _, _ *corev1.Pod) {
			node.Spec.Taints = []corev1.Taint{{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule}}
		}, wantDelete: true},
		{name: "resource failure must not conceal a port conflict", mutate: func(_ *corev1.Node, occupant, pending *corev1.Pod) {
			ports := []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}}
			occupant.Spec.Containers[0].Ports = ports
			pending.Spec.Containers[0].Ports = ports
		}, wantDelete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme, err := newClientScheme()
			if err != nil {
				t.Fatal(err)
			}
			group := &sched.PodGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "gang", Namespace: "test"},
				Spec:       sched.PodGroupSpec{MinMember: 48},
			}
			objects := []kubeclient.Object{group}
			var members []*corev1.Pod
			for i := range 48 {
				p := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("member%d", i), Namespace: "test", UID: ktypes.UID(fmt.Sprintf("member%d", i)),
						Labels:      map[string]string{sched.PodGroupLabel: "gang", wellknown.LabelExternalJobId: "42"},
						Annotations: map[string]string{wellknown.AnnotationExternalJobNode: fmt.Sprintf("node%d", i)},
					},
					Spec: corev1.PodSpec{
						SchedulerName: schedulerName,
						Containers: []corev1.Container{{Name: "work", Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
						}}},
					},
				}
				// Model a gang with 18 members already bound when the next member
				// encounters resources still occupied by the preceding allocation.
				if i < 18 {
					p.Spec.NodeName = p.Annotations[wellknown.AnnotationExternalJobNode]
				}
				members = append(members, p)
				objects = append(objects, p)
			}
			pending := members[18]
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node18"},
				Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("110"),
				}},
			}
			occupant := pending.DeepCopy()
			occupant.Name, occupant.UID, occupant.Spec.NodeName = "previous", "previous", node.Name
			occupant.Labels = map[string]string{wellknown.LabelExternalJobId: "1"}
			if tt.mutate != nil {
				tt.mutate(node, occupant, pending)
			}
			occupants := []*corev1.Pod{occupant}
			if tt.occupantCount > 1 {
				occupant.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("100m")
				for i := 1; i < tt.occupantCount; i++ {
					p := occupant.DeepCopy()
					p.Name = fmt.Sprintf("previous%d", i)
					p.UID = ktypes.UID(p.Name)
					p.Labels[wellknown.LabelExternalJobId] = strconv.Itoa(i + 1)
					occupants = append(occupants, p)
				}
			}
			snapshot := internalcache.NewSnapshot(occupants, []*corev1.Node{node})
			activator := &activateRecorder{}
			f, err := tf.NewFramework(ctx, []tf.RegisterPluginFunc{
				tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
				tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
				tf.RegisterPluginAsExtensions(noderesources.Name, fwkruntime.FactoryAdapter(feature.Features{}, noderesources.NewFit), "PreFilter", "Filter"),
				tf.RegisterFilterPlugin(tainttoleration.Name, fwkruntime.FactoryAdapter(feature.Features{}, tainttoleration.New)),
				tf.RegisterPluginAsExtensions(nodeports.Name, fwkruntime.FactoryAdapter(feature.Features{}, nodeports.New), "PreFilter", "Filter"),
				tf.RegisterFilterPlugin(Name, func(context.Context, runtime.Object, fwk.Handle) (fwk.Plugin, error) { return &SlurmBridge{}, nil }),
			}, schedulerName,
				fwkruntime.WithInformerFactory(informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0)),
				fwkruntime.WithSnapshotSharedLister(snapshot), fwkruntime.WithPodActivator(activator),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			state := framework.NewCycleState()
			if _, status, _ := f.RunPreFilterPlugins(ctx, state, pending); !status.IsSuccess() {
				t.Fatalf("PreFilter: %v", status)
			}
			info, err := snapshot.NodeInfos().Get(node.Name)
			if err != nil {
				t.Fatal(err)
			}
			filterStatus := f.RunFilterPlugins(ctx, state, pending, info)
			if filterStatus.Plugin() != noderesources.Name {
				t.Fatalf("expected original resource rejection, got %v", filterStatus)
			}
			kubeClient := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			slurmObjects := []object.Object{&types.V0044JobInfo{V0044JobInfo: api.V0044JobInfo{
				JobId: ptr.To(int32(42)), Nodes: ptr.To("node[0-47]"),
				JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
			}}}
			jobState := tt.jobState
			if jobState == "" {
				jobState = api.V0044JobInfoJobStateCOMPLETED
			}
			if !tt.missingJob {
				for i, p := range occupants {
					var comment *string
					if !tt.missingPods {
						namespace := p.Namespace
						if tt.jobNamespace != "" {
							namespace = tt.jobNamespace
						}
						info := externaljobinfo.ExternalJobInfo{Pods: []string{namespace + "/" + p.Name}}
						comment = ptr.To(info.ToString())
					}
					slurmObjects = append(slurmObjects, &types.V0044JobInfo{V0044JobInfo: api.V0044JobInfo{
						JobId: ptr.To(int32(i + 1)), JobState: &[]api.V0044JobInfoJobState{jobState},
						AdminComment: comment,
					}})
				}
			}
			baseSlurm := fake.NewClientBuilder().WithObjects(slurmObjects...).Build()
			deletes, gets, lists := 0, 0, 0
			slurm := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, list object.ObjectList, opts ...slurmclient.ListOption) error {
					lists++
					return baseSlurm.List(ctx, list, opts...)
				},
				Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...slurmclient.GetOption) error {
					gets++
					if key != object.ObjectKey("42") {
						t.Fatalf("unexpected occupant job lookup: %v", key)
					}
					return baseSlurm.Get(ctx, key, obj, opts...)
				},
				Delete: func(context.Context, object.Object, ...slurmclient.DeleteOption) error {
					deletes++
					return nil
				},
			}).Build()
			sb := &SlurmBridge{
				Client: kubeClient, schedulerName: schedulerName,
				slurmControl: slurmcontrol.NewControl(slurm, "kubernetes", schedulerName), handle: f,
			}
			if _, status := sb.PreFilter(ctx, state, pending, nil); !status.IsSuccess() {
				t.Fatalf("SlurmBridge PreFilter: %v", status)
			}
			m := framework.NewNodeToStatus(map[string]*fwk.Status{node.Name: filterStatus}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable))
			_, status := sb.PostFilter(ctx, state, pending, m)
			if lists != 1 || gets != 1 {
				t.Errorf("Slurm reads: %d lists, %d gets; want one PreFilter list and one PostFilter get", lists, gets)
			}
			if tt.wantDelete {
				if deletes != 1 || !status.IsSuccess() {
					t.Fatalf("PostFilter: deletes=%d, status=%v; want one cancellation", deletes, status)
				}
				return
			}
			if deletes != 0 || status.Code() != fwk.Unschedulable {
				t.Fatalf("PostFilter: deletes=%d, status=%v; want to wait without cancellation", deletes, status)
			}
			if len(activator.pods) != 0 {
				t.Fatal("waiting for resource release must not immediately reactivate the pod")
			}
			for _, member := range members {
				got := &corev1.Pod{}
				if err := kubeClient.Get(ctx, kubeclient.ObjectKeyFromObject(member), got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got.Labels, member.Labels) || !reflect.DeepEqual(got.Annotations, member.Annotations) || got.Spec.NodeName != member.Spec.NodeName {
					t.Errorf("gang member %s lost its allocation or binding", member.Name)
				}
			}
			// Binding becomes feasible using the original allocation once the old
			// occupant leaves; no metadata repair or new submission is needed.
			for _, p := range occupants {
				if err := info.RemovePod(klog.FromContext(ctx), p); err != nil {
					t.Fatal(err)
				}
			}
			if status := f.RunFilterPlugins(ctx, state, pending, info); !status.IsSuccess() {
				t.Fatalf("Filter after previous allocation leaves: %v", status)
			}
		})
	}
}

// Both shared allocations are RUNNING, with a bound member on opposite nodes.
// Each bound member waits for its gang's other member, so neither allocation
// will finish while PostFilter preserves both allocations.
func TestPostFilterSharedGangsDoNotWaitForEachOther(t *testing.T) {
	metrics.Register()
	ctx := context.Background()
	const schedulerName = "slurm-bridge"
	scheme, err := newClientScheme()
	if err != nil {
		t.Fatal(err)
	}
	makeMember := func(name, group, id, assigned, bound string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "test", UID: ktypes.UID(name),
				Labels: map[string]string{sched.PodGroupLabel: group, wellknown.LabelExternalJobId: id},
				Annotations: map[string]string{
					wellknown.AnnotationExternalJobNode:   assigned,
					"slurmjob.slinky.slurm.net/exclusive": "false",
				},
			},
			Spec: corev1.PodSpec{SchedulerName: schedulerName, NodeName: bound,
				Containers: []corev1.Container{{Name: "work", Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				}}},
			},
		}
	}
	a0 := makeMember("a0", "gang-a", "42", "node0", "node0")
	a1 := makeMember("a1", "gang-a", "42", "node1", "")
	b0 := makeMember("b0", "gang-b", "43", "node0", "")
	b1 := makeMember("b1", "gang-b", "43", "node1", "node1")
	objects := []kubeclient.Object{a0, a1, b0, b1}
	for _, group := range []string{"gang-a", "gang-b"} {
		objects = append(objects, &sched.PodGroup{
			ObjectMeta: metav1.ObjectMeta{Name: group, Namespace: "test"},
			Spec:       sched.PodGroupSpec{MinMember: 2},
		})
	}
	kubeClient := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	var nodes []*corev1.Node
	for _, name := range []string{"node0", "node1"} {
		nodes = append(nodes, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: corev1.NodeStatus{
				Capacity:    corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourcePods: resource.MustParse("110")},
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m"), corev1.ResourcePods: resource.MustParse("110")},
			},
		})
	}
	snapshot := internalcache.NewSnapshot([]*corev1.Pod{a0, b1}, nodes)
	f, err := tf.NewFramework(ctx, []tf.RegisterPluginFunc{
		tf.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
		tf.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
		tf.RegisterPluginAsExtensions(noderesources.Name, fwkruntime.FactoryAdapter(feature.Features{}, noderesources.NewFit), "PreFilter", "Filter"),
		tf.RegisterFilterPlugin(Name, func(context.Context, runtime.Object, fwk.Handle) (fwk.Plugin, error) { return &SlurmBridge{}, nil }),
	}, schedulerName,
		fwkruntime.WithInformerFactory(informers.NewSharedInformerFactory(clientsetfake.NewClientset(), 0)),
		fwkruntime.WithSnapshotSharedLister(snapshot), fwkruntime.WithPodActivator(&activateRecorder{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	deletes := 0
	slurm := fake.NewClientBuilder().WithObjects(
		&types.V0044JobInfo{V0044JobInfo: api.V0044JobInfo{
			JobId: ptr.To(int32(42)), Nodes: ptr.To("node[0-1]"),
			JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
			AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{Pods: []string{"test/a0", "test/a1"}}).ToString()),
		}},
		&types.V0044JobInfo{V0044JobInfo: api.V0044JobInfo{
			JobId: ptr.To(int32(43)), Nodes: ptr.To("node[0-1]"),
			JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
			AdminComment: ptr.To((&externaljobinfo.ExternalJobInfo{Pods: []string{"test/b0", "test/b1"}}).ToString()),
		}},
	).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(context.Context, object.Object, ...slurmclient.DeleteOption) error { deletes++; return nil },
	}).Build()
	sb := &SlurmBridge{Client: kubeClient, schedulerName: schedulerName,
		slurmControl: slurmcontrol.NewControl(slurm, "kubernetes", schedulerName), handle: f,
	}
	for _, pending := range []*corev1.Pod{a1, b0} {
		state := framework.NewCycleState()
		if _, status, _ := f.RunPreFilterPlugins(ctx, state, pending); !status.IsSuccess() {
			t.Fatal(status)
		}
		if _, status := sb.PreFilter(ctx, state, pending, nil); !status.IsSuccess() {
			t.Fatal(status)
		}
		assigned := pending.Annotations[wellknown.AnnotationExternalJobNode]
		info, err := snapshot.NodeInfos().Get(assigned)
		if err != nil {
			t.Fatal(err)
		}
		filterStatus := f.RunFilterPlugins(ctx, state, pending, info)
		if filterStatus.Plugin() != noderesources.Name {
			t.Fatalf("expected resource rejection: %v", filterStatus)
		}
		statuses := framework.NewNodeToStatus(map[string]*fwk.Status{assigned: filterStatus}, fwk.NewStatus(fwk.UnschedulableAndUnresolvable))
		_, status := sb.PostFilter(ctx, state, pending, statuses)
		if !status.IsSuccess() {
			t.Fatalf("%s: PostFilter=%v; want cancellation for retry", pending.Name, status)
		}
	}
	if deletes == 0 {
		t.Fatal("both gangs retain their allocations and wait for the other gang to finish: neither can bind its missing member")
	}
}
