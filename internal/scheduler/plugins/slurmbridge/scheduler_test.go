// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

var _ = Describe("Scheduler RBAC", func() {
	Context("Translating a Deployment-owned pod under real RBAC enforcement", func() {
		It("walks the owner chain Pod -> ReplicaSet -> Deployment without RBAC errors", func() {
			ns := corev1.NamespaceDefault
			labels := map[string]string{"app": "rbac-int-test"}

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "rbac-int-deploy", Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: labels},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "sleep", Image: "busybox"}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			rs := &appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rbac-int-rs",
					Namespace: ns,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deployment.Name,
						UID:        deployment.UID,
						Controller: ptr.To(true),
					}},
				},
				Spec: appsv1.ReplicaSetSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: deployment.Spec.Template,
				},
			}
			Expect(k8sClient.Create(ctx, rs)).To(Succeed())

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rbac-int-pod",
					Namespace: ns,
					Labels:    labels,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       rs.Name,
						UID:        rs.UID,
						Controller: ptr.To(true),
					}},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "sleep", Image: "busybox"}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			ir, err := slurmjobir.TranslateToSlurmJobIR(schedulerClient, ctx, pod)
			Expect(err).NotTo(HaveOccurred(),
				"scheduler ServiceAccount must be able to read every owner-Kind reachable from a Pod")
			Expect(ir).NotTo(BeNil())
			Expect(ir.RootPOM.Kind).To(Equal("Deployment"))
			Expect(ir.RootPOM.Name).To(Equal(deployment.Name))
		})
	})
})
