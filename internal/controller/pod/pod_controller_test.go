// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"context"
	"net/http"

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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	v0043 "github.com/SlinkyProject/slurm-client/api/v0043"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"
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
			controllerReconciler := New(k8sClient, k8sClient.Scheme(), eventCh, slurmClient)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When generating pod events", func() {
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

		It("should generate a single pod event", func() {
			By("calling generateEvents for jobId 1")
			eventCh := make(chan event.GenericEvent)
			jobList := &slurmtypes.V0043JobInfoList{
				Items: []slurmtypes.V0043JobInfo{
					{
						V0043JobInfo: v0043.V0043JobInfo{
							JobId: ptr.To[int32](1),
						},
					},
				},
			}
			slurmClient := slurmclientfake.NewClientBuilder().WithLists(jobList).Build()
			controllerReconciler := New(k8sClient, k8sClient.Scheme(), eventCh, slurmClient)

			go func() {
				controllerReconciler.generatePodEvents(1, false)
				close(eventCh)
			}()
			events := 0
			for range eventCh {
				events++
			}
			Expect(events).To(Equal(1))
		})

		It("should generate a single pod event and delete the job", func() {
			By("calling generateEvents for jobId 2 with no pods")
			eventCh := make(chan event.GenericEvent)
			jobList := &slurmtypes.V0043JobInfoList{
				Items: []slurmtypes.V0043JobInfo{
					{
						V0043JobInfo: v0043.V0043JobInfo{
							JobId: ptr.To[int32](2),
						},
					},
				},
			}
			slurmClient := slurmclientfake.NewClientBuilder().WithLists(jobList).Build()
			controllerReconciler := New(k8sClient, k8sClient.Scheme(), eventCh, slurmClient)
			controllerReconciler.generatePodEvents(2, true)
			job := &slurmtypes.V0043JobInfo{}
			err := slurmClient.Get(ctx, object.ObjectKey("2"), job)
			Expect(err.Error()).To(Equal(http.StatusText(http.StatusNotFound)))
		})
	})
})
