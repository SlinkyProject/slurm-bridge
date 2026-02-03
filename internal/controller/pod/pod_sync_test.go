// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"context"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclientfake "github.com/SlinkyProject/slurm-client/pkg/client/fake"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/pod/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func newPlaceholderInfo(name string) *placeholderinfo.PlaceholderInfo {
	return &placeholderinfo.PlaceholderInfo{
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
						wellknown.LabelPlaceholderJobId: strconv.Itoa(int(jobId)),
					}
				}
				return nil
			}(),
		},
		Spec: corev1.PodSpec{
			SchedulerName: schedulerName,
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

func newRequest(name string) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
		},
	}
}

var _ = Describe("syncKubernetes()", func() {
	var controller *PodReconciler

	podName := "foo"
	req := newRequest(podName)
	var jobId int32 = 1

	BeforeEach(func() {
		jobList := &slurmtypes.V0044JobInfoList{
			Items: []slurmtypes.V0044JobInfo{
				{
					V0044JobInfo: api.V0044JobInfo{
						JobId:        ptr.To(jobId),
						JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
						AdminComment: ptr.To(newPlaceholderInfo(podName).ToString()),
					},
				},
				{
					V0044JobInfo: api.V0044JobInfo{
						JobId:        ptr.To[int32](2),
						JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
						AdminComment: ptr.To(newPlaceholderInfo("bar").ToString()),
					},
				},
			},
		}
		c := slurmclientfake.NewClientBuilder().WithLists(jobList).Build()
		podList := &corev1.PodList{
			Items: []corev1.Pod{
				*newPod(podName, jobId),
				*newPod("bar", 0),
			},
		}
		controller = &PodReconciler{
			Client:        fake.NewFakeClient(podList),
			SchedulerName: schedulerName,
			Scheme:        scheme.Scheme,
			SlurmClient:   c,
			EventCh:       make(chan event.GenericEvent, 5),
			slurmControl:  slurmcontrol.NewControl(c),
			eventRecorder: record.NewFakeRecorder(10),
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
})

var _ = Describe("syncSlurm()", func() {
	var controller *PodReconciler

	podName := "foo"
	req := newRequest(podName)
	var jobId int32 = 1

	BeforeEach(func() {
		jobList := &slurmtypes.V0044JobInfoList{
			Items: []slurmtypes.V0044JobInfo{
				{
					V0044JobInfo: api.V0044JobInfo{
						JobId:        ptr.To(jobId),
						JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
						AdminComment: ptr.To(newPlaceholderInfo(podName).ToString()),
					},
				},
				{
					V0044JobInfo: api.V0044JobInfo{
						JobId:        ptr.To[int32](2),
						JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
						AdminComment: ptr.To(newPlaceholderInfo("bar").ToString()),
					},
				},
			},
		}
		c := slurmclientfake.NewClientBuilder().WithLists(jobList).Build()
		podList := &corev1.PodList{
			Items: []corev1.Pod{
				*newPod("foo", jobId),
				{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "bar",
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "2",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: schedulerName,
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodSucceeded,
					},
				},
			},
		}
		controller = &PodReconciler{
			Client:        fake.NewFakeClient(podList),
			Scheme:        scheme.Scheme,
			SlurmClient:   c,
			EventCh:       make(chan event.GenericEvent, 5),
			slurmControl:  slurmcontrol.NewControl(c),
			eventRecorder: record.NewFakeRecorder(10),
		}
	})

	Context("With pods and jobs", func() {
		It("Should not terminate the job", func() {
			By("Reconciling")
			err := controller.syncSlurm(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: podName}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())

			By("Check job existence")
			exists, err := controller.slurmControl.IsJobRunning(ctx, pod)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeTrue())
		})

		It("Should terminate the job", func() {
			By("Terminating the corresponding Slurm job")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: "bar"}
			pod := &corev1.Pod{}
			err := controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).ToNot(BeTrue())

			By("Reconciling")
			err = controller.syncSlurm(ctx, newRequest("bar"))
			Expect(err).NotTo(HaveOccurred())

			By("Check job is not running")
			exists, err := controller.slurmControl.IsJobRunning(ctx, pod)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeFalse())
		})
	})
})

var _ = Describe("Sync()", func() {
	ctx := context.Background()
	var controller *PodReconciler

	podName := "foo"
	req := newRequest(podName)
	var jobId int32 = 1

	BeforeEach(func() {
		jobList := &slurmtypes.V0044JobInfoList{
			Items: []slurmtypes.V0044JobInfo{
				{
					V0044JobInfo: api.V0044JobInfo{
						JobId:        ptr.To(jobId),
						JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
						AdminComment: ptr.To(newPlaceholderInfo(podName).ToString()),
					},
				},
				{
					V0044JobInfo: api.V0044JobInfo{
						JobId:        ptr.To[int32](2),
						JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
						AdminComment: ptr.To(newPlaceholderInfo("bar").ToString()),
					},
				},
			},
		}
		c := slurmclientfake.NewClientBuilder().WithLists(jobList).Build()
		podList := &corev1.PodList{
			Items: []corev1.Pod{
				*newPod("foo", jobId),
				*newPod("bar", 2),
			},
		}
		controller = &PodReconciler{
			Client:        fake.NewFakeClient(podList),
			Scheme:        scheme.Scheme,
			SchedulerName: schedulerName,
			SlurmClient:   c,
			EventCh:       make(chan event.GenericEvent, 5),
			slurmControl:  slurmcontrol.NewControl(c),
			eventRecorder: record.NewFakeRecorder(10),
		}
	})

	Context("With pods and jobs", func() {
		It("Should not terminate the pod or job", func() {
			By("Reconciling")
			err := controller.Sync(ctx, req)
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
			err = controller.Sync(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			By("Check pod existence")
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: podName}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("Should terminate the job", func() {
			By("Terminating the corresponding Kubernetes pod")
			err := controller.Delete(ctx, newPod(podName, jobId))
			Expect(err).ToNot(HaveOccurred())
			key := types.NamespacedName{Namespace: corev1.NamespaceDefault, Name: podName}
			pod := &corev1.Pod{}
			err = controller.Get(ctx, key, pod)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("Reconciling")
			err = controller.Sync(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("prepareTerminalPod()", func() {
	var controller *PodReconciler

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
							wellknown.LabelPlaceholderJobId: "1",
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
		controller = &PodReconciler{
			Client:        fake.NewFakeClient(podList, claim),
			Scheme:        scheme.Scheme,
			SlurmClient:   c,
			EventCh:       make(chan event.GenericEvent, 5),
			slurmControl:  slurmcontrol.NewControl(c),
			eventRecorder: record.NewFakeRecorder(10),
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
	})
})
