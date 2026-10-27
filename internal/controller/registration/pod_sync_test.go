// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/registration/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func newExternalJobInfo(name string) *externaljobinfo.ExternalJobInfo {
	return &externaljobinfo.ExternalJobInfo{
		Pods: []string{name},
	}
}

func newPod(name string, jobId int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
			Labels: func() map[string]string {
				if jobId != 0 {
					return map[string]string{
						wellknown.LabelExternalJobId: strconv.Itoa(int(jobId)),
					}
				}
				return nil
			}(),
		},
		Spec: corev1.PodSpec{
			SchedulerName: schedulerName,
			NodeName:      "test-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.PodReady,
					LastTransitionTime: metav1.Now(),
					Status:             corev1.ConditionTrue,
				},
			},
		},
	}
}

// newQueuedPod returns a pod that has not been bound yet (awaiting
// placement).
func newQueuedPod(name string, jobId int32) *corev1.Pod {
	pod := newPod(name, jobId)
	pod.Spec.NodeName = ""
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = nil
	return pod
}

// newNotReadyPod returns a pod bound to a Slurm job that is Running but not
// Ready (e.g. crash-looping).
func newNotReadyPod(name string, jobId int32) *corev1.Pod {
	pod := newPod(name, jobId)
	pod.Status.Conditions = []corev1.PodCondition{
		{
			Type:               corev1.PodReady,
			LastTransitionTime: metav1.Now(),
			Status:             corev1.ConditionFalse,
		},
	}
	return pod
}

func newTerminatingPod(name string, jobId int32) *corev1.Pod {
	pod := newPod(name, jobId)
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	return pod
}

func newTerminalPod(name string, jobId int32) *corev1.Pod {
	pod := newPod(name, jobId)
	pod.Status.Phase = corev1.PodSucceeded
	return pod
}

func newRequest(name string) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
		},
	}
}

var _ = Describe("syncKubernetes()", func() {
	var controller *Reconciler

	podName := "foo"
	req := newRequest(podName)
	var jobId int32 = 1

	BeforeEach(func() {
		jobList := &api.V0044JobInfoObjectList{
			Items: []api.V0044JobInfo{
				{

					JobId:        ptr.To(jobId),
					JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
					AdminComment: ptr.To(newExternalJobInfo(podName).ToString()),
				},
				{

					JobId:        ptr.To[int32](2),
					JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
					AdminComment: ptr.To(newExternalJobInfo("bar").ToString()),
				},
				{

					JobId:        ptr.To[int32](3),
					JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
					AdminComment: ptr.To(newExternalJobInfo("crashloop").ToString()),
				},
				{

					JobId: ptr.To[int32](4),
					JobState: &[]api.V0044JobInfoJobState{
						api.V0044JobInfoJobStatePENDING,
						api.V0044JobInfoJobStatePREEMPTED,
					},
					AdminComment: ptr.To(newExternalJobInfo("requeued").ToString()),
				},
				{

					JobId: ptr.To[int32](5),
					JobState: &[]api.V0044JobInfoJobState{
						api.V0044JobInfoJobStatePENDING,
						api.V0044JobInfoJobStatePREEMPTED,
					},
					AdminComment: ptr.To(newExternalJobInfo("requeued-bound").ToString()),
				},
				{

					JobId:        ptr.To[int32](6),
					JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateSUSPENDED},
					AdminComment: ptr.To(newExternalJobInfo("suspended").ToString()),
				},
			},
		}
		c := slurmclientfake.NewClientBuilder().WithLists(jobList).Build()
		podList := &corev1.PodList{
			Items: []corev1.Pod{
				*newPod(podName, jobId),
				*newPod("bar", 0),
				*newQueuedPod("unlabeled-queued", 0),
				*newNotReadyPod("crashloop", 3),
				*newQueuedPod("queued", 4),
				*newNotReadyPod("requeued-bound", 5),
				*newPod("suspended", 6),
				*newQueuedPod("queued-dead", 7),
			},
		}
		controller = &Reconciler{
			Client:        fake.NewFakeClient(podList),
			schedulerName: schedulerName,
			slurmControl:  slurmcontrol.NewControl(c),
		}
	})

	Context("With pods and jobs", func() {
		It("Should not terminate the pod", func() {
			By("Reconciling")
			err := controller.syncKubernetes(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: podName}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())
		})

		It("Should terminate the pod", func() {
			By("Terminating the corresponding Slurm job")
			err := controller.slurmControl.TerminateJob(ctx, jobId)
			Expect(err).ToNot(HaveOccurred())

			By("Reconciling")
			err = controller.syncKubernetes(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: podName}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("With a not-Ready (crash-looping) pod", func() {
		// Regression: a crash-looping pod is never Ready, but its Slurm job
		// can still be preempted/canceled. It must be deleted like any
		// other pod, or its ResourceClaim keeps a device allocation that
		// Slurm has already handed to another pod (device double-booking).
		It("Should terminate the pod when its job is gone", func() {
			By("Terminating the corresponding Slurm job")
			err := controller.slurmControl.TerminateJob(ctx, 3)
			Expect(err).ToNot(HaveOccurred())

			By("Reconciling")
			err = controller.syncKubernetes(ctx, newRequest("crashloop"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "crashloop"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("Should not terminate the pod while its job is running", func() {
			By("Reconciling")
			err := controller.syncKubernetes(ctx, newRequest("crashloop"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "crashloop"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())
		})

		It("Should terminate a bound pod whose job was requeued (stale binding)", func() {
			By("Reconciling")
			err := controller.syncKubernetes(ctx, newRequest("requeued-bound"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "requeued-bound"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("Should terminate a pod whose job is suspended", func() {
			By("Reconciling")
			err := controller.syncKubernetes(ctx, newRequest("suspended"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "suspended"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("With unbound pods", func() {
		// Unbound pods are owned by the scheduler and never reconciled,
		// whatever the state of their job (or lack of one).
		It("Should not terminate a queued pod while its job is pending", func() {
			By("Reconciling")
			err := controller.syncKubernetes(ctx, newRequest("queued"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "queued"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())
		})

		It("Should not terminate a queued pod whose job is gone", func() {
			By("Reconciling")
			err := controller.syncKubernetes(ctx, newRequest("queued-dead"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "queued-dead"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())
		})

		It("Should not terminate an unlabeled queued pod", func() {
			By("Reconciling")
			err := controller.syncKubernetes(ctx, newRequest("unlabeled-queued"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "unlabeled-queued"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())
		})
	})

	Context("With a bound pod not labeled with a Slurm job", func() {
		It("Should terminate the pod (orphan without a job)", func() {
			By("Reconciling the orphan pod")
			err := controller.syncKubernetes(ctx, newRequest("bar"))
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "bar"}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})

var _ = Describe("prepareTerminalPod()", func() {
	var controller *Reconciler

	podName := "foo"
	req := newRequest(podName)

	BeforeEach(func() {
		c := slurmclientfake.NewClientBuilder().Build()
		podList := &corev1.PodList{
			Items: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      podName,
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
						Finalizers: []string{wellknown.FinalizerScheduler},
					},
					Spec: corev1.PodSpec{
						SchedulerName: schedulerName,
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodSucceeded,
						ExtendedResourceClaimStatus: &corev1.PodExtendedResourceClaimStatus{
							ResourceClaimName: "foo",
						},
					},
				},
			},
		}
		claim := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo",
				Namespace: metav1.NamespaceDefault,
			},
		}
		controller = &Reconciler{
			Client:       fake.NewFakeClient(podList, claim),
			slurmControl: slurmcontrol.NewControl(c),
		}
	})

	Context("With pods and jobs", func() {
		It("Should not terminate the job", func() {
			By("Reconciling")
			err := controller.prepareTerminalPod(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			podKey := types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: podName}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, podKey, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())

			By("Check finalizer does not exist")
			Expect(pod.ObjectMeta.Finalizers).To(BeEmpty())

			By("Check ResourceClaim does not exist")
			claimKey := types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: podName}
			claim := &resourcev1.ResourceClaim{}
			err = controller.Get(ctx, claimKey, claim)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("Should not remove finalizer for terminating pod", func() {
			By("Creating a non-terminal terminating pod with finalizer and claim")
			podName := "terminating"
			terminatingPod := newTerminatingPod(podName, 1)
			terminatingPod.Finalizers = []string{wellknown.FinalizerScheduler}
			terminatingPod.Status.ExtendedResourceClaimStatus = &corev1.PodExtendedResourceClaimStatus{
				ResourceClaimName: "terminating-claim",
			}
			claim := &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "terminating-claim",
					Namespace: metav1.NamespaceDefault,
				},
			}
			localController := &Reconciler{
				Client:       fake.NewFakeClient(terminatingPod, claim),
				slurmControl: slurmcontrol.NewControl(slurmclientfake.NewFakeClient()),
			}

			By("Reconciling")
			err := localController.prepareTerminalPod(ctx, newRequest(podName))
			Expect(err).NotTo(HaveOccurred())

			By("Check finalizer still exists")
			podKey := types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: podName}
			pod := &corev1.Pod{}
			err = localController.Get(ctx, podKey, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())
			Expect(pod.ObjectMeta.Finalizers).To(Equal([]string{wellknown.FinalizerScheduler}))

			By("Check ResourceClaim still exists")
			claimKey := types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "terminating-claim"}
			gotClaim := &resourcev1.ResourceClaim{}
			err = localController.Get(ctx, claimKey, gotClaim)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
