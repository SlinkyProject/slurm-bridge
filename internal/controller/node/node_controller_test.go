// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v0043 "github.com/SlinkyProject/slurm-client/api/v0043"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"
)

const (
	schedulerName = "slurm-bridge-scheduler"
)

var _ = Describe("Node Controller", func() {
	Context("SetupWithManager()", func() {
		It("Should initialize successfully", func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme})
			Expect(err).ToNot(HaveOccurred())

			eventCh := make(chan event.GenericEvent)
			slurmclient := slurmclientfake.NewFakeClient()
			r := &NodeReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				EventCh:       eventCh,
				SlurmClient:   slurmclient,
				eventRecorder: record.NewFakeRecorder(10),
			}
			err = r.SetupWithManager(mgr)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		node := &corev1.Node{}

		BeforeEach(func() {
			By("creating the resource for the Kind Node")
			err := k8sClient.Get(ctx, typeNamespacedName, node)
			if err != nil && errors.IsNotFound(err) {
				resource := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &corev1.Node{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Node")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			eventCh := make(chan event.TypedGenericEvent[client.Object])
			slurmClient := slurmclientfake.NewFakeClient()
			controllerReconciler := New(k8sClient, k8sClient.Scheme(), schedulerName, eventCh, slurmClient)
			Expect(controllerReconciler).NotTo(BeNil())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			eventCh := make(chan event.TypedGenericEvent[client.Object])
			list := &slurmtypes.V0043NodeList{
				Items: []slurmtypes.V0043Node{
					{V0043Node: v0043.V0043Node{Name: ptr.To(resourceName)}},
					{V0043Node: v0043.V0043Node{Name: ptr.To("node-0")}},
				},
			}
			slurmClient := slurmclientfake.NewClientBuilder().WithLists(list).Build()
			controllerReconciler := New(k8sClient, k8sClient.Scheme(), schedulerName, eventCh, slurmClient)
			Expect(controllerReconciler).NotTo(BeNil())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
