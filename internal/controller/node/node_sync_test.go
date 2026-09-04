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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/util/taints"
	"k8s.io/utils/ptr"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

var _ = Describe("nodeRegistrationInventories()", func() {
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

var _ = Describe("syncTaint()", func() {
	var controllerReconciler *NodeReconciler

	BeforeEach(func() {
		nodeList := &corev1.NodeList{
			Items: []corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "kube-0"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "bridged-0"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "annotated-1", Labels: map[string]string{wellknown.LabelSlurmNodeName: "bridged-1"}}},
			},
		}
		k8sClient := fake.NewFakeClient(nodeList)
		Expect(k8sClient).NotTo(BeNil())

		slurmNodeList := &slurmtypes.V0044NodeList{
			Items: []slurmtypes.V0044Node{
				{V0044Node: api.V0044Node{Name: ptr.To("slurm-0")}},
				{V0044Node: api.V0044Node{Name: ptr.To("bridged-0")}},
				{V0044Node: api.V0044Node{Name: ptr.To("bridged-1")}},
			},
		}
		slurmClient := slurmclientfake.NewClientBuilder().WithLists(slurmNodeList).Build()
		Expect(slurmClient).NotTo(BeNil())

		eventCh := make(chan event.GenericEvent)
		controllerReconciler = NewReconciler(k8sClient, slurmClient, schedulerName, eventCh, nil)
		Expect(controllerReconciler).NotTo(BeNil())
	})

	Context("Taint and untaint Kubernetes nodes", func() {
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

		slurmNodeList := &slurmtypes.V0044NodeList{
			Items: []slurmtypes.V0044Node{
				{V0044Node: api.V0044Node{Name: ptr.To("slurm-0")}},
				{V0044Node: api.V0044Node{Name: ptr.To("bridged-0")}},
				{V0044Node: api.V0044Node{Name: ptr.To("bridged-1")}},
				{V0044Node: api.V0044Node{Name: ptr.To("bridged-2")}},
				{V0044Node: api.V0044Node{Name: ptr.To("bridged-3")}},
			},
		}
		updateFn := func(_ context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
			switch o := obj.(type) {
			case *slurmtypes.V0044Node:
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
			err := controllerReconciler.syncState(ctx, req)
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
			err := controllerReconciler.syncState(ctx, req)
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
			err := controllerReconciler.syncState(ctx, req)
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
			err := controllerReconciler.syncState(ctx, req)
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
			err := controllerReconciler.syncState(ctx, req)
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
			err := controllerReconciler.syncState(ctx, req)
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
			WithObjects(&slurmtypes.V0044Node{V0044Node: api.V0044Node{
				Name: ptr.To(node.Name),
				Gres: ptr.To("gpu:gpu-example:1,nic:infiniband:1"),
			}}).
			WithUpdateFn(updateFn).
			Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), nil)

		err := r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).NotTo(HaveOccurred())
		Expect(gotExtra).To(Equal(
			`slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu-example":["/dra/gpu.example.com/hybrid-0/gpu-0"]}}`,
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
			WithObjects(&slurmtypes.V0044Node{V0044Node: api.V0044Node{
				Name: ptr.To(node.Name),
				Gres: ptr.To("gpu:gpu-example:2"),
			}}).
			Build()
		r := NewReconciler(kubeClient, slurmClient, schedulerName, make(chan event.GenericEvent), nil)
		eventRecorder := record.NewFakeRecorder(1)
		r.eventRecorder = eventRecorder

		err := r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).To(MatchError(ContainSubstring("incompatible with required DRA GRES")))
		updatedNode := &corev1.Node{}
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		condition := findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(corev1.ConditionFalse))
		Expect(condition.Reason).To(Equal(reasonIncompatibleSlurmGRES))
		Expect(condition.Message).To(ContainSubstring("NodeName=hybrid-0 Name=gpu Type=gpu-example Count=1"))
		Expect(eventRecorder.Events).To(Receive(And(
			ContainSubstring("IncompatibleSlurmGRES"),
			ContainSubstring("NodeName=hybrid-0 Name=gpu Type=gpu-example Count=1"),
		)))

		err = r.syncNodeRegistration(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).To(MatchError(ContainSubstring("incompatible with required DRA GRES")))
		Consistently(eventRecorder.Events).ShouldNot(Receive())
	})

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

		Expect(r.syncNodeRegistration(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: node.Name},
		})).To(Succeed())
		updatedNode := &corev1.Node{}
		Expect(kubeClient.Get(ctx, client.ObjectKeyFromObject(node), updatedNode)).To(Succeed())
		Expect(findNodeCondition(updatedNode.Status.Conditions, wellknown.NodeConditionSlurmGRESCompatible)).To(BeNil())
		Expect(findNodeCondition(updatedNode.Status.Conditions, corev1.NodeReady)).NotTo(BeNil())
	})
})
