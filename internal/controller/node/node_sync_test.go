// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/util/taints"
	"k8s.io/utils/ptr"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/testutils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

var _ = Describe("nodeRegistrationInventories()", func() {
	It("passes socket CPU topology into hybrid registration", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "hybrid-cpu"}}
		slice := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "hybrid-cpu"},
			Spec: resourcev1.ResourceSliceSpec{
				Driver: nodeinfo.DraDriverCpu, NodeName: ptr.To(node.Name),
				Pool: resourcev1.ResourcePool{Name: node.Name, Generation: 1, ResourceSliceCount: 1},
				Devices: []resourcev1.Device{{
					Name: "socket000", AllowMultipleAllocations: ptr.To(true),
					Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						nodeinfo.DraDriverCpu_SocketID:   {IntValue: ptr.To[int64](0)},
						nodeinfo.DraDriverCpu_SMTEnabled: {BoolValue: ptr.To(false)},
						nodeinfo.DraDriverCpu_CPUIDs:     {StringValue: ptr.To("0-3")},
						nodeinfo.DraDriverCpu_SMTLayout:  {StringValue: ptr.To("0-3")},
					},
					Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{nodeinfo.DraDriverCpu_Capacity: {Value: resource.MustParse("4")}},
				}},
			},
		}
		kubeClient := fake.NewClientBuilder().WithObjects(node, slice).Build()
		slurmClient := slurmclientfake.NewClientBuilder().WithObjects(&api.V0044Node{
			Name: ptr.To(node.Name), Cpus: ptr.To[int32](4), Sockets: ptr.To[int32](1), Cores: ptr.To[int32](4), Threads: ptr.To[int32](1),
			State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE}),
		}).WithUpdateFn(func(_ context.Context, obj object.Object, req any, _ ...slurmclient.UpdateOption) error {
			update := req.(api.V0044UpdateNodeMsg)
			Expect(update.State).To(BeNil())
			current := obj.(*api.V0044Node)
			if update.Extra != nil {
				current.Extra = update.Extra
			}
			if update.Features != nil {
				current.Features = update.Features
			}
			if update.FeaturesAct != nil {
				current.ActiveFeatures = update.FeaturesAct
			}
			return nil
		}).Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), nil)
		_, err := r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(node)})
		Expect(err).NotTo(HaveOccurred())
		registered := &api.V0044Node{}
		Expect(slurmClient.Get(ctx, object.ObjectKey(node.Name), registered)).To(Succeed())
		info, err := nodeinfo.NewNodeInfoFromResourceSlices(node.Name, []resourcev1.ResourceSlice{*slice})
		Expect(err).NotTo(HaveOccurred())
		Expect(info.ValidateCPUFingerprint(ptr.Deref(registered.Extra, ""))).To(Succeed())
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
		condition := findNodeCondition(node.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(corev1.ConditionTrue))
	})

	It("reports overlapping DRA profiles on the Node", func() {
		profiles := []dra.DeviceProfile{
			{
				Name:     "gpu-a",
				Driver:   "gpu.example.com",
				Selector: `device.driver == "gpu.example.com"`,
				Backend:  dra.IndexedGRESBackend{GRESName: "gpu"},
			},
			{
				Name:     "gpu-b",
				Driver:   "gpu.example.com",
				Selector: `device.driver == "gpu.example.com" && true`,
				Backend:  dra.IndexedGRESBackend{GRESName: "gpu"},
			},
		}
		registry, err := dra.NewRegistry(profiles)
		Expect(err).NotTo(HaveOccurred())

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
		resourceSlice := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "gpu-pool-a"},
			Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.example.com",
				NodeName: ptr.To("node-a"),
				Pool: resourcev1.ResourcePool{
					Name:               "pool-a",
					Generation:         1,
					ResourceSliceCount: 1,
				},
				Devices: []resourcev1.Device{{Name: "gpu-0"}},
			},
		}
		recorder := record.NewFakeRecorder(1)
		r := &NodeReconciler{
			Client:        fake.NewFakeClient(resourceSlice),
			draRegistry:   registry,
			eventRecorder: recorder,
		}

		_, _, err = r.nodeRegistrationInventories(context.Background(), node)
		Expect(err).To(MatchError(ContainSubstring(`matches overlapping device profiles "gpu-a" and "gpu-b"`)))
		Eventually(recorder.Events).Should(Receive(Equal(
			`Warning OverlappingDRADeviceProfiles DRA device "gpu.example.com/pool-a/gpu-0" matches overlapping device profiles "gpu-a" and "gpu-b"`,
		)))
	})
})

type patchCountingClient struct {
	client.Client
	patchCalls int
}

func (c *patchCountingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.patchCalls++
	return c.Client.Patch(ctx, obj, patch, opts...)
}

var _ = Describe("Sync()", func() {
	It("cancels a registration drain when the external-node label is restored", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "external-0"}}
		kubeClient := fake.NewFakeClient(node)

		updateFn := func(_ context.Context, obj object.Object, req any, _ ...slurmclient.UpdateOption) error {
			slurmNode := obj.(*api.V0044Node)
			update := req.(api.V0044UpdateNodeMsg)
			state := set.New(ptr.Deref(slurmNode.State, []api.V0044NodeState{})...)
			for _, requested := range ptr.Deref(update.State, []api.V0044UpdateNodeMsgState{}) {
				switch requested {
				case api.V0044UpdateNodeMsgStateUNDRAIN:
					state.Delete(api.V0044NodeStateDRAIN)
				default:
					state.Insert(api.V0044NodeState(requested))
				}
			}
			slurmNode.State = ptr.To(state.UnsortedList())
			if update.Reason != nil {
				slurmNode.Reason = update.Reason
			}
			return nil
		}
		slurmClient := slurmclientfake.NewClientBuilder().
			WithObjects(&api.V0044Node{
				Name:           ptr.To(node.Name),
				State:          ptr.To([]api.V0044NodeState{api.V0044NodeStateALLOCATED, api.V0044NodeStateDRAIN, api.V0044NodeStateEXTERNAL}),
				Reason:         ptr.To("slurm-bridge: removing external node " + node.Name),
				Features:       ptr.To(api.V0044CsvString{wellknown.SlurmFeatureGRESCompatible}),
				ActiveFeatures: ptr.To(api.V0044CsvString{wellknown.SlurmFeatureGRESCompatible}),
			}).
			WithUpdateFn(updateFn).
			Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), nil)
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}}
		DeferCleanup(func() { durationStore.Pop(req.String()) })

		Expect(r.Sync(ctx, req)).To(MatchError(ContainSubstring("draining before inventory replacement")))
		drained, err := r.slurmControl.IsNodeDrain(ctx, node)
		Expect(err).NotTo(HaveOccurred())
		Expect(drained).To(BeTrue())

		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
		node.Labels = map[string]string{wellknown.LabelExternalNode: "true"}
		Expect(kubeClient.Update(ctx, node)).To(Succeed())

		Expect(r.Sync(ctx, req)).To(Succeed())
		drained, err = r.slurmControl.IsNodeDrain(ctx, node)
		Expect(err).NotTo(HaveOccurred())
		Expect(drained).To(BeFalse())
	})
})

var _ = Describe("syncTaint()", func() {
	var controllerReconciler *NodeReconciler
	var k8sClient *patchCountingClient

	BeforeEach(func() {
		nodeList := &corev1.NodeList{
			Items: []corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "kube-0"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "bridged-0"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "annotated-1", Labels: map[string]string{wellknown.LabelSlurmNodeName: "bridged-1"}}},
			},
		}
		k8sClient = &patchCountingClient{Client: fake.NewFakeClient(nodeList)}
		Expect(k8sClient).NotTo(BeNil())

		slurmNodeList := &api.V0044NodeObjectList{
			Items: []api.V0044Node{
				{Name: ptr.To("slurm-0")},
				{Name: ptr.To("bridged-0")},
				{Name: ptr.To("bridged-1")},
			},
		}
		slurmClient := slurmclientfake.NewClientBuilder().WithLists(slurmNodeList).Build()
		Expect(slurmClient).NotTo(BeNil())

		eventCh := make(chan event.GenericEvent)
		controllerReconciler = NewReconciler(k8sClient, slurmClient, schedulerName, eventCh, nil)
		Expect(controllerReconciler).NotTo(BeNil())
	})

	Context("Taint and untaint Kubernetes nodes", func() {
		DescribeTable("preserves concurrent taint changes", func(nodeName string, initiallyTainted bool) {
			bridgeTaint := utils.NewTaintNodeBridged(schedulerName)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			if initiallyTainted {
				node.Spec.Taints = []corev1.Taint{*bridgeTaint}
			}
			concurrentTaint := corev1.Taint{
				Key:    corev1.TaintNodeUnreachable,
				Effect: corev1.TaintEffectNoSchedule,
			}
			updated := false
			k8sClient.Client = fake.NewClientBuilder().WithObjects(node).WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if !updated {
						// Another controller changes the Node after our read, before our patch.
						latest := &corev1.Node{}
						if err := c.Get(ctx, client.ObjectKeyFromObject(obj), latest); err != nil {
							return err
						}
						latest.Spec.Taints = append(latest.Spec.Taints, concurrentTaint)
						if err := c.Update(ctx, latest); err != nil {
							return err
						}
						updated = true
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			}).Build()
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(node)}

			By("rejecting the stale patch without losing the concurrent taint")
			err := controllerReconciler.syncTaint(ctx, req)
			Expect(apierrors.IsConflict(err)).To(BeTrue(), "expected a conflict, got %v", err)
			latest := &corev1.Node{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, latest)).To(Succeed())
			Expect(taints.TaintExists(latest.Spec.Taints, &concurrentTaint)).To(BeTrue())
			Expect(taints.TaintExists(latest.Spec.Taints, bridgeTaint)).To(Equal(initiallyTainted))

			By("retrying against fresh state and preserving the concurrent taint")
			Expect(controllerReconciler.syncTaint(ctx, req)).To(Succeed())
			Expect(k8sClient.Get(ctx, req.NamespacedName, latest)).To(Succeed())
			Expect(taints.TaintExists(latest.Spec.Taints, &concurrentTaint)).To(BeTrue())
			Expect(taints.TaintExists(latest.Spec.Taints, bridgeTaint)).To(Equal(!initiallyTainted))
			Expect(k8sClient.patchCalls).To(Equal(2))

			By("skipping the patch once the taints are reconciled")
			Expect(controllerReconciler.syncTaint(ctx, req)).To(Succeed())
			Expect(k8sClient.patchCalls).To(Equal(2))
		},
			Entry("when adding the bridge taint", "bridged-0", false),
			Entry("when removing the bridge taint", "kube-0", true),
		)

		It("Should untaint Kubernetes node", func() {
			By("syncTaint()")
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "kube-0",
				},
			}
			err := controllerReconciler.syncTaint(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check node taints")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: "kube-0"}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			taint := utils.NewTaintNodeBridged(schedulerName)
			isTainted := taints.TaintExists(checkNode.Spec.Taints, taint)
			Expect(isTainted).To(BeFalse())
			Expect(k8sClient.patchCalls).To(Equal(0))
		})

		It("Should taint bridged node", func() {
			By("syncTaint()")
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "bridged-0",
				},
			}
			err := controllerReconciler.syncTaint(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check node taints")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: "bridged-0"}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			taint := utils.NewTaintNodeBridged(schedulerName)
			isTainted := taints.TaintExists(checkNode.Spec.Taints, taint)
			Expect(isTainted).To(BeTrue())
			Expect(k8sClient.patchCalls).To(Equal(1))

			By("syncTaint() again")
			err = controllerReconciler.syncTaint(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.patchCalls).To(Equal(1))
		})

		It("Should taint bridged node with annotation", func() {
			By("syncTaint()")
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "annotated-1",
				},
			}
			err := controllerReconciler.syncTaint(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check node taints")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: "annotated-1"}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			taint := utils.NewTaintNodeBridged(schedulerName)
			isTainted := taints.TaintExists(checkNode.Spec.Taints, taint)
			Expect(isTainted).To(BeTrue())
		})

		It("Should ignore Slurm node", func() {
			By("syncTaint()")
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "slurm-0",
				},
			}
			err := controllerReconciler.syncTaint(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check node taints")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: "slurm-0"}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("syncState()", func() {
	var controllerReconciler *NodeReconciler

	BeforeEach(func() {
		nodeList := &corev1.NodeList{
			Items: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "kube-0"},
					Spec:       corev1.NodeSpec{Unschedulable: false},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "kube-1"},
					Spec:       corev1.NodeSpec{Unschedulable: true},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "bridged-0"},
					Spec:       corev1.NodeSpec{Unschedulable: false},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "bridged-1"},
					Spec:       corev1.NodeSpec{Unschedulable: true},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "annotated-2",
						Labels: map[string]string{
							wellknown.LabelSlurmNodeName: "bridged-2",
						},
					},
					Spec: corev1.NodeSpec{Unschedulable: false},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "annotated-3",
						Labels: map[string]string{
							wellknown.LabelSlurmNodeName: "bridged-3",
						},
					},
					Spec: corev1.NodeSpec{Unschedulable: true},
				},
			},
		}
		k8sClient := fake.NewFakeClient(nodeList)
		Expect(k8sClient).NotTo(BeNil())

		slurmNodeList := &api.V0044NodeObjectList{
			Items: []api.V0044Node{
				{Name: ptr.To("slurm-0")},
				{Name: ptr.To("bridged-0")},
				{Name: ptr.To("bridged-1")},
				{Name: ptr.To("bridged-2")},
				{Name: ptr.To("bridged-3")},
			},
		}
		updateFn := func(_ context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
			switch o := obj.(type) {
			case *api.V0044Node:
				r, ok := req.(api.V0044UpdateNodeMsg)
				if !ok {
					return errors.New("failed to cast request object")
				}
				stateSet := set.New(ptr.Deref(o.State, []api.V0044NodeState{})...)
				statesReq := ptr.Deref(r.State, []api.V0044UpdateNodeMsgState{})
				for _, stateReq := range statesReq {
					switch stateReq {
					case api.V0044UpdateNodeMsgStateUNDRAIN:
						stateSet.Delete(api.V0044NodeStateDRAIN)
					default:
						stateSet.Insert(api.V0044NodeState(stateReq))
					}
				}
				o.State = ptr.To(stateSet.UnsortedList())
				o.Comment = r.Comment
				o.Extra = r.Extra
				o.Reason = r.Reason
			default:
				return errors.New("failed to cast slurm object")
			}
			return nil
		}
		slurmClient := slurmclientfake.NewClientBuilder().WithUpdateFn(updateFn).WithLists(slurmNodeList).Build()
		Expect(slurmClient).NotTo(BeNil())

		eventCh := make(chan event.GenericEvent)
		controllerReconciler = NewReconciler(k8sClient, slurmClient, schedulerName, eventCh, nil)
		Expect(controllerReconciler).NotTo(BeNil())
	})

	Context("Drain or Undrain Slurm nodes", func() {
		It("Should ignore Kubernetes node", func() {
			By("syncState()")
			nodeName := "kube-0"
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			}
			err := controllerReconciler.syncState(ctx, req, true)
			Expect(err).NotTo(HaveOccurred())

			By("Check node status")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: nodeName}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			isUnschedulable := checkNode.Spec.Unschedulable
			Expect(isUnschedulable).To(BeFalse())
			_, err = controllerReconciler.slurmControl.IsNodeDrain(ctx, checkNode)
			Expect(err).To(HaveOccurred())
		})

		It("Should ignore Kubernetes node, again", func() {
			By("syncState()")
			nodeName := "kube-1"
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			}
			err := controllerReconciler.syncState(ctx, req, true)
			Expect(err).NotTo(HaveOccurred())

			By("Check node status")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: nodeName}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			isUnschedulable := checkNode.Spec.Unschedulable
			Expect(isUnschedulable).To(BeTrue())
			_, err = controllerReconciler.slurmControl.IsNodeDrain(ctx, checkNode)
			Expect(err).To(HaveOccurred())
		})

		It("Should undrain Slurm node", func() {
			By("syncState()")
			nodeName := "bridged-0"
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			}
			err := controllerReconciler.syncState(ctx, req, true)
			Expect(err).NotTo(HaveOccurred())

			By("Check node status")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: nodeName}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			isUnschedulable := checkNode.Spec.Unschedulable
			Expect(isUnschedulable).To(BeFalse())
			isDrain, err := controllerReconciler.slurmControl.IsNodeDrain(ctx, checkNode)
			Expect(err).ToNot(HaveOccurred())
			Expect(isDrain).To(BeFalse())
		})

		It("Should drain Slurm node", func() {
			By("syncState()")
			nodeName := "bridged-1"
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			}
			err := controllerReconciler.syncState(ctx, req, true)
			Expect(err).NotTo(HaveOccurred())

			By("Check node status")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: nodeName}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			isUnschedulable := checkNode.Spec.Unschedulable
			Expect(isUnschedulable).To(BeTrue())
			isDrain, err := controllerReconciler.slurmControl.IsNodeDrain(ctx, checkNode)
			Expect(err).ToNot(HaveOccurred())
			Expect(isDrain).To(BeTrue())
		})

		It("Should undrain Slurm node", func() {
			By("syncState()")
			nodeName := "annotated-2"
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			}
			err := controllerReconciler.syncState(ctx, req, true)
			Expect(err).NotTo(HaveOccurred())

			By("Check node status")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: nodeName}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			isUnschedulable := checkNode.Spec.Unschedulable
			Expect(isUnschedulable).To(BeFalse())
			isDrain, err := controllerReconciler.slurmControl.IsNodeDrain(ctx, checkNode)
			Expect(err).ToNot(HaveOccurred())
			Expect(isDrain).To(BeFalse())
		})

		It("Should drain Slurm node", func() {
			By("syncState()")
			nodeName := "annotated-3"
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			}
			err := controllerReconciler.syncState(ctx, req, true)
			Expect(err).NotTo(HaveOccurred())

			By("Check node status")
			checkNode := &corev1.Node{}
			objKey := client.ObjectKey{Name: nodeName}
			err = controllerReconciler.Get(ctx, objKey, checkNode)
			Expect(err).NotTo(HaveOccurred())
			isUnschedulable := checkNode.Spec.Unschedulable
			Expect(isUnschedulable).To(BeTrue())
			isDrain, err := controllerReconciler.slurmControl.IsNodeDrain(ctx, checkNode)
			Expect(err).ToNot(HaveOccurred())
			Expect(isDrain).To(BeTrue())
		})
	})
})

var _ = Describe("syncNodeRegistration() hybrid nodes", func() {
	It("patches the bridge-owned DRA inventory on an unlabeled hybrid node", func() {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "hybrid-0"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}}},
		}
		resourceSlice := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "hybrid-0-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.example.com",
				NodeName: ptr.To(node.Name),
				Pool: resourcev1.ResourcePool{
					Name:               node.Name,
					Generation:         1,
					ResourceSliceCount: 1,
				},
				Devices: []resourcev1.Device{{Name: "gpu-0"}},
			},
		}
		kubeClient := fake.NewClientBuilder().WithObjects(node, resourceSlice).Build()

		var gotExtra string
		updateFn := func(_ context.Context, _ object.Object, req any, _ ...slurmclient.UpdateOption) error {
			update := req.(api.V0044UpdateNodeMsg)
			if update.Extra != nil {
				gotExtra = *update.Extra
			}
			return nil
		}
		slurmClient := slurmclientfake.NewClientBuilder().
			WithObjects(&api.V0044Node{
				Name: ptr.To(node.Name),
				Gres: ptr.To("gpu:gpu.example.com:1,nic:infiniband:1"),
			}).
			WithUpdateFn(updateFn).
			Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), testutils.DRARegistryWithExampleGPU())

		_, err := r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).NotTo(HaveOccurred())
		Expect(gotExtra).To(Equal(
			`slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":{"firstIndex":0,"devices":["/dra/gpu.example.com/hybrid-0/gpu-0"]}}}`,
		))
		updatedNode := &corev1.Node{}
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		condition := findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(corev1.ConditionTrue))
		Expect(condition.Reason).To(Equal(reasonSlurmGRESCompatible))
		Expect(findNodeCondition(updatedNode.Status.Conditions, corev1.NodeReady)).NotTo(BeNil())
	})

	It("publishes the required gres.conf inventory when the hybrid node is incompatible", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "hybrid-0"}}
		resourceSlice := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "hybrid-0-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.example.com",
				NodeName: ptr.To(node.Name),
				Pool: resourcev1.ResourcePool{
					Name:               node.Name,
					Generation:         1,
					ResourceSliceCount: 1,
				},
				Devices: []resourcev1.Device{{Name: "gpu-0"}},
			},
		}
		kubeClient := fake.NewClientBuilder().WithObjects(node, resourceSlice).Build()
		slurmClient := slurmclientfake.NewClientBuilder().
			WithObjects(&api.V0044Node{
				Name: ptr.To(node.Name),
				Gres: ptr.To("gpu:gpu.example.com:2"),
			}).
			Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), testutils.DRARegistryWithExampleGPU())

		_, err := r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).To(MatchError(ContainSubstring("incompatible with required DRA GRES")))
		updatedNode := &corev1.Node{}
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		condition := findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(corev1.ConditionFalse))
		Expect(condition.Reason).To(Equal(reasonIncompatibleSlurmGRES))
		Expect(condition.Message).To(ContainSubstring("NodeName=hybrid-0 Name=gpu Type=gpu.example.com Count=1"))

		_, err = r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).To(MatchError(ContainSubstring("incompatible with required DRA GRES")))
	})

	DescribeTable("removes the compatibility feature when DRA inventory cannot be verified", func(labeled, external, failUpdate bool) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "hybrid-0"},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
				Conditions: []corev1.NodeCondition{{
					Type:   wellknown.NodeConditionSlurmGRESCompatible,
					Status: corev1.ConditionTrue,
				}},
			},
		}
		if labeled {
			node.Labels = map[string]string{wellknown.LabelExternalNode: "true"}
		}
		resourceSlice := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "hybrid-0-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.example.com",
				NodeName: ptr.To(node.Name),
				Pool: resourcev1.ResourcePool{
					Name:               node.Name,
					Generation:         1,
					ResourceSliceCount: 2,
				},
				Devices: []resourcev1.Device{{Name: "gpu-0"}},
			},
		}
		kubeClient := fake.NewClientBuilder().WithObjects(node, resourceSlice).Build()

		var updates []api.V0044UpdateNodeMsg
		updateFn := func(_ context.Context, obj object.Object, req any, _ ...slurmclient.UpdateOption) error {
			update := req.(api.V0044UpdateNodeMsg)
			updates = append(updates, update)
			if failUpdate {
				return errors.New("feature update failed")
			}
			slurmNode := obj.(*api.V0044Node)
			if update.Features != nil {
				slurmNode.Features = update.Features
			}
			if update.FeaturesAct != nil {
				slurmNode.ActiveFeatures = update.FeaturesAct
			}
			if update.State != nil {
				Expect(*update.State).To(Equal([]api.V0044UpdateNodeMsgState{api.V0044UpdateNodeMsgStateDRAIN}))
				slurmNode.State = ptr.To(append(*slurmNode.State, api.V0044NodeStateDRAIN))
			}
			return nil
		}
		state := []api.V0044NodeState{api.V0044NodeStateIDLE}
		if external {
			state = append(state, api.V0044NodeStateEXTERNAL)
		}
		appliedInventory := `slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":{"firstIndex":0,"devices":["/dra/gpu.example.com/hybrid-0/gpu-0"]}}}`
		slurmClient := slurmclientfake.NewClientBuilder().
			WithObjects(&api.V0044Node{
				Name:           ptr.To(node.Name),
				Cpus:           ptr.To(int32(4)),
				RealMemory:     ptr.To(int64(8192)),
				Gres:           ptr.To("gpu:gpu.example.com:1"),
				State:          ptr.To(state),
				Features:       ptr.To(api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible}),
				ActiveFeatures: ptr.To(api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible}),
				Extra:          ptr.To(appliedInventory),
			}).
			WithUpdateFn(updateFn).
			Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), testutils.DRARegistryWithExampleGPU())

		_, err := r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).To(MatchError(ContainSubstring("generation 1 is incomplete: found 1 of 2 ResourceSlices")))
		if failUpdate {
			Expect(err).To(MatchError(ContainSubstring("feature update failed")))
			updatedNode := &corev1.Node{}
			Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
			condition := findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(corev1.ConditionUnknown))
			failUpdate = false
			updates = nil
			_, err = r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
			Expect(err).To(MatchError(ContainSubstring("generation 1 is incomplete: found 1 of 2 ResourceSlices")))
		}
		if external {
			Expect(updates).To(HaveLen(3))
			Expect(updates[0].State).To(Equal(ptr.To([]api.V0044UpdateNodeMsgState{api.V0044UpdateNodeMsgStateDRAIN})))
			Expect(updates[0].Extra).To(BeNil())
			updates = updates[1:]
			state = append(state, api.V0044NodeStateDRAIN)
		}
		Expect(updates).To(HaveLen(2))
		Expect(updates[0].FeaturesAct).NotTo(BeNil())
		Expect(*updates[0].FeaturesAct).To(Equal(api.V0044CsvString{"admin-feature"}))
		Expect(updates[0].Extra).To(BeNil())
		Expect(updates[1].Features).NotTo(BeNil())
		Expect(*updates[1].Features).To(Equal(api.V0044CsvString{"admin-feature"}))
		Expect(updates[1].Extra).To(BeNil())

		updatedNode := &corev1.Node{}
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		condition := findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(corev1.ConditionUnknown))
		Expect(condition.Reason).To(Equal(reasonSlurmGRESVerificationError))

		By("preserving the Slurm node and its last applied inventory")
		slurmNode := &api.V0044Node{}
		Expect(slurmClient.Get(ctx, object.ObjectKey(node.Name), slurmNode)).To(Succeed())
		Expect(*slurmNode.State).To(Equal(state))
		Expect(*slurmNode.Gres).To(Equal("gpu:gpu.example.com:1"))
		Expect(*slurmNode.Extra).To(Equal(appliedInventory))
		Expect(*slurmNode.Features).To(Equal(api.V0044CsvString{"admin-feature"}))
		Expect(*slurmNode.ActiveFeatures).To(Equal(api.V0044CsvString{"admin-feature"}))

		By("restoring eligibility after the pool becomes complete")
		resourceSlice.Spec.Pool.ResourceSliceCount = 1
		Expect(kubeClient.Update(ctx, resourceSlice)).To(Succeed())
		_, err = r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).NotTo(HaveOccurred())
		Expect(slurmClient.Get(ctx, object.ObjectKey(node.Name), slurmNode)).To(Succeed())
		Expect(*slurmNode.Features).To(ConsistOf("admin-feature", wellknown.SlurmFeatureGRESCompatible))
		Expect(*slurmNode.ActiveFeatures).To(ConsistOf("admin-feature", wellknown.SlurmFeatureGRESCompatible))
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		condition = findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
		if labeled {
			Expect(condition).To(BeNil())
		} else {
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(corev1.ConditionTrue))
		}
	},
		Entry("unlabeled hybrid node", false, false, false),
		Entry("labeled hybrid node", true, false, false),
		Entry("external node", true, true, false),
		Entry("external node retries a failed feature update", true, true, true),
	)

	It("clears the compatibility condition when the node is no longer hybrid", func() {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "not-hybrid"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
				{
					Type:   wellknown.NodeConditionSlurmGRESCompatible,
					Status: corev1.ConditionFalse,
				},
			}},
		}
		kubeClient := fake.NewClientBuilder().WithObjects(node).Build()
		r := NewReconciler(
			kubeClient,
			slurmclientfake.NewClientBuilder().Build(),
			schedulerName,
			make(chan event.GenericEvent),
			nil,
		)

		_, err := r.syncNodeRegistration(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: node.Name},
		})
		Expect(err).NotTo(HaveOccurred())
		updatedNode := &corev1.Node{}
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		Expect(findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)).To(BeNil())
		Expect(findNodeCondition(updatedNode.Status.Conditions, corev1.NodeReady)).NotTo(BeNil())
	})
})

var _ = Describe("syncNodeRegistration() labeled hybrid nodes", func() {
	It("removes the compatibility feature when a labeled slurmd-registered node is incompatible", func() {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "hybrid-0",
				Labels: map[string]string{wellknown.LabelExternalNode: "true"},
			},
		}
		resourceSlice := &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "hybrid-0-gpus"},
			Spec: resourcev1.ResourceSliceSpec{
				Driver:   "gpu.example.com",
				NodeName: ptr.To(node.Name),
				Pool: resourcev1.ResourcePool{
					Name:               node.Name,
					Generation:         1,
					ResourceSliceCount: 1,
				},
				Devices: []resourcev1.Device{{Name: "gpu-0"}},
			},
		}
		kubeClient := fake.NewClientBuilder().WithObjects(node, resourceSlice).Build()

		var updates []api.V0044UpdateNodeMsg
		updateFn := func(_ context.Context, _ object.Object, req any, _ ...slurmclient.UpdateOption) error {
			updates = append(updates, req.(api.V0044UpdateNodeMsg))
			return nil
		}
		slurmClient := slurmclientfake.NewClientBuilder().
			WithObjects(&api.V0044Node{
				Name:           ptr.To(node.Name),
				Gres:           ptr.To("gpu:gpu.example.com:2"),
				Features:       ptr.To(api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible}),
				ActiveFeatures: ptr.To(api.V0044CsvString{"admin-feature", wellknown.SlurmFeatureGRESCompatible}),
			}).
			WithUpdateFn(updateFn).
			Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), testutils.DRARegistryWithExampleGPU())

		_, err := r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).To(MatchError(ContainSubstring("incompatible with required DRA GRES")))
		Expect(updates).To(HaveLen(2))
		Expect(*updates[0].FeaturesAct).To(Equal(api.V0044CsvString{"admin-feature"}))
		Expect(*updates[1].Features).To(Equal(api.V0044CsvString{"admin-feature"}))

		updatedNode := &corev1.Node{}
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		condition := findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(corev1.ConditionFalse))
		Expect(condition.Reason).To(Equal(reasonIncompatibleSlurmGRES))
	})
})
