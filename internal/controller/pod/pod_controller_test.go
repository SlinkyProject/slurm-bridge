// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
)

const (
	schedulerName = "slurm-bridge-scheduler"
)

var _ = Describe("Pod Controller", func() {
	Context("SetupWithManager()", func() {
		It("Should initialize successfully", func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme})
			Expect(err).ToNot(HaveOccurred())

			r := &PodReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				EventCh:       make(chan event.GenericEvent),
				SlurmClient:   slurmclientfake.NewFakeClient(),
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
			Namespace: corev1.NamespaceDefault,
		}
		pod := &corev1.Pod{}

		BeforeEach(func() {
			By("creating the resource for the Kind Pod")
			err := k8sClient.Get(ctx, typeNamespacedName, pod)
			if err != nil && errors.IsNotFound(err) {
				// Ref: https://k8s.io/examples/pods/simple-pod.yaml
				resource := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: corev1.NamespaceDefault,
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "nginx",
								Image: "nginx:1.14.2",
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: 80,
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &corev1.Pod{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err != nil && !errors.IsNotFound(err) {
				By("Cleanup the specific resource instance Pod")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			eventCh := make(chan event.GenericEvent)
			slurmClient := slurmclientfake.NewFakeClient()
			controllerReconciler := NewReconciler(k8sClient, slurmClient, schedulerName, eventCh)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
