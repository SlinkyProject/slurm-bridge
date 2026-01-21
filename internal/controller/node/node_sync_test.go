// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

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
		controllerReconciler = NewReconciler(k8sClient, slurmClient, schedulerName, eventCh)
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
				o.Reason = r.Reason
			default:
				return errors.New("failed to cast slurm object")
			}
			return nil
		}
		slurmClient := slurmclientfake.NewClientBuilder().WithUpdateFn(updateFn).WithLists(slurmNodeList).Build()
		Expect(slurmClient).NotTo(BeNil())

		eventCh := make(chan event.GenericEvent)
		controllerReconciler = NewReconciler(k8sClient, slurmClient, schedulerName, eventCh)
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
