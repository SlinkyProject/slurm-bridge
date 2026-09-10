// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestTranslateToSlurmJobIR_BasicPodGroupJobs(t *testing.T) {
	for _, version := range []string{WorkloadAPIVersionV1Alpha2, WorkloadAPIVersionV1Beta1} {
		t.Run(version, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(corev1.AddToScheme(scheme))
			utilruntime.Must(batchv1.AddToScheme(scheme))
			utilruntime.Must(jobset.AddToScheme(scheme))
			api := mustRegisterWorkloadAPI(t, scheme, version)
			for _, tt := range []struct {
				name         string
				siblingPhase corev1.PodPhase
				jobSetOwner  bool
			}{
				{name: "sequential Job after completion", siblingPhase: corev1.PodSucceeded},
				{name: "replacement Job pod after failure", siblingPhase: corev1.PodFailed},
				{name: "parallel Job with a running sibling", siblingPhase: corev1.PodRunning},
				{name: "JobSet retains per-pod scheduling", siblingPhase: corev1.PodRunning, jobSetOwner: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					ctx := context.Background()
					pg := newPodGroup("workers", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
						Basic: &schedulingv1alpha2.BasicSchedulingPolicy{},
					})
					pg.TypeMeta = api.PodGroupTypeMeta
					pg.Annotations = map[string]string{
						wellknown.AnnotationAccount:   "group-account",
						wellknown.AnnotationPartition: "group-partition",
						wellknown.AnnotationQOS:       "group-qos",
					}
					if version == WorkloadAPIVersionV1Alpha2 {
						pg.Spec.PodGroupTemplateRef = &schedulingv1alpha2.PodGroupTemplateReference{
							Workload: &schedulingv1alpha2.WorkloadPodGroupTemplateReference{WorkloadName: "workload"},
						}
					} else {
						pg.Spec.WorkloadRef = &WorkloadReference{WorkloadName: "workload"}
					}
					workload := &Workload{
						TypeMeta: metav1.TypeMeta{APIVersion: api.PodGroupTypeMeta.APIVersion, Kind: "Workload"},
						ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "default", Annotations: map[string]string{
							wellknown.AnnotationQOS: "workload-qos",
						}},
					}
					job := jobWithAnnotations("job", "default", map[string]string{
						wellknown.AnnotationPartition: "job-partition",
						wellknown.AnnotationQOS:       "job-qos",
					})
					job.Spec.Parallelism = ptr.To[int32](1)
					job.Spec.Completions = ptr.To[int32](3)
					job.Spec.ActiveDeadlineSeconds = ptr.To[int64](120)
					pod := podWithJobOwner(podWithSchedulingGroup("default", "pending", pg.Name), job.Name)
					pod.Labels = map[string]string{batchv1.JobNameLabel: job.Name, "job-name": job.Name}
					pod.Annotations = map[string]string{wellknown.AnnotationWckey: "ignored-pod-wckey"}
					sibling := pod.DeepCopy()
					sibling.Name = "previous"
					sibling.Status.Phase = tt.siblingPhase
					sibling.Labels[wellknown.LabelExternalJobId] = "old-allocation"
					objects := []client.Object{pg, workload, job, pod, sibling}
					wantRoot := job_v1
					wantPartition := "job-partition"
					if tt.jobSetOwner {
						owner := &jobset.JobSet{
							TypeMeta: jobSet_v1alpha2,
							ObjectMeta: metav1.ObjectMeta{Name: "jobset", Namespace: "default", Annotations: map[string]string{
								wellknown.AnnotationPartition: "jobset-partition",
								wellknown.AnnotationQOS:       "jobset-qos",
							}},
						}
						job.OwnerReferences = []metav1.OwnerReference{controllerOwner(owner.APIVersion, owner.Kind, owner.Name)}
						objects = append(objects, owner)
						wantRoot = jobSet_v1alpha2
						wantPartition = "jobset-partition"
					}
					cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
					ir, err := TranslateToSlurmJobIR(cl, dra.DefaultRegistry(), api, ctx, pod)
					if err != nil {
						t.Fatal(err)
					}
					if ir.RootPOM.TypeMeta != wantRoot {
						t.Errorf("root = %v, want %v", ir.RootPOM.TypeMeta, wantRoot)
					}
					if len(ir.Pods.Items) != 1 || ir.Pods.Items[0].Name != pod.Name || ptr.Deref(ir.JobInfo.MinNodes, 0) != 1 {
						t.Errorf("allocation includes siblings: pods=%v minNodes=%v", ir.Pods.Items, ir.JobInfo.MinNodes)
					}
					if ptr.Deref(ir.JobInfo.TimeLimit, 0) != 2 {
						t.Errorf("TimeLimit = %v, want Job deadline translated to 2 minutes", ir.JobInfo.TimeLimit)
					}
					if ptr.Deref(ir.JobInfo.Account, "") != "group-account" || ptr.Deref(ir.JobInfo.Partition, "") != wantPartition || ptr.Deref(ir.JobInfo.QOS, "") != "workload-qos" || ir.JobInfo.Wckey != nil {
						t.Errorf("annotation precedence changed: %#v", ir.JobInfo)
					}
					if status := PreFilter(cl, dra.DefaultRegistry(), api, ctx, pod, ir); !status.IsSuccess() {
						t.Errorf("Basic Job should schedule independently: %v", status)
					}
					if ptr.Deref(pod.Spec.SchedulingGroup.PodGroupName, "") != pg.Name {
						t.Error("translation changed the Pod's scheduling-group reference")
					}
				})
			}
		})
	}
}

func TestTranslateToSlurmJobIR_BasicPodGroupOwnerScheduling(t *testing.T) {
	for _, version := range []string{WorkloadAPIVersionV1Alpha2, WorkloadAPIVersionV1Beta1} {
		t.Run(version, func(t *testing.T) {
			for _, lwsOwner := range []bool{false, true} {
				name := "standalone Pod"
				if lwsOwner {
					name = "LeaderWorkerSet"
				}
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					scheme := runtime.NewScheme()
					utilruntime.Must(corev1.AddToScheme(scheme))
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					api := mustRegisterWorkloadAPI(t, scheme, version)
					pg := newPodGroup("workers", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{Basic: &schedulingv1alpha2.BasicSchedulingPolicy{}})
					pod := podWithSchedulingGroup("default", "p1", pg.Name)
					pod.Annotations = map[string]string{wellknown.AnnotationPartition: "pod-partition"}
					pod.Labels = map[string]string{lwsv1.GroupUniqueHashLabelKey: "group-0"}
					objects := []client.Object{pg, pod}
					wantRoot, wantPods, wantMaxNodes := pod_v1, 1, int32(1)
					wantPartition := "pod-partition"
					if lwsOwner {
						owner := newLWS("lws", 2)
						owner.TypeMeta = lws_v1
						owner.Annotations = map[string]string{wellknown.AnnotationPartition: "lws-partition"}
						pod.OwnerReferences = []metav1.OwnerReference{controllerOwner(owner.APIVersion, owner.Kind, owner.Name)}
						objects = append(objects, owner)
						wantRoot, wantPods, wantMaxNodes = lws_v1, 2, 2
						wantPartition = "lws-partition"
					}
					sibling := pod.DeepCopy()
					sibling.Name = "p2"
					objects = append(objects, sibling)
					cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
					ir, err := TranslateToSlurmJobIR(cl, dra.DefaultRegistry(), api, ctx, pod)
					if err != nil {
						t.Fatal(err)
					}
					if ir.RootPOM.TypeMeta != wantRoot || len(ir.Pods.Items) != wantPods || ptr.Deref(ir.JobInfo.MaxNodes, 0) != wantMaxNodes {
						t.Errorf("owner scheduling changed: root=%v pods=%d maxNodes=%v", ir.RootPOM.TypeMeta, len(ir.Pods.Items), ir.JobInfo.MaxNodes)
					}
					if ptr.Deref(ir.JobInfo.Partition, "") != wantPartition {
						t.Errorf("Partition = %v, want %q", ir.JobInfo.Partition, wantPartition)
					}
					if lwsOwner {
						// Basic PodGroup membership must not bypass LWS readiness.
						ir.Pods.Items = ir.Pods.Items[:1]
						if status := PreFilter(cl, dra.DefaultRegistry(), api, ctx, pod, ir); status.IsSuccess() {
							t.Error("incomplete LWS group passed PreFilter")
						}
					}
				})
			}
		})
	}
}

func TestTranslateToSlurmJobIR_GangPodGroupReadiness(t *testing.T) {
	for _, version := range []string{WorkloadAPIVersionV1Alpha2, WorkloadAPIVersionV1Beta1} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			utilruntime.Must(corev1.AddToScheme(scheme))
			api := mustRegisterWorkloadAPI(t, scheme, version)
			pg := newPodGroup("workers", "default", schedulingv1alpha2.PodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
			})
			pod := podWithSchedulingGroup("default", "p1", pg.Name)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pg, pod).Build()
			for _, ready := range []bool{false, true} {
				if ready {
					if err := cl.Create(ctx, podWithSchedulingGroup("default", "p2", pg.Name)); err != nil {
						t.Fatal(err)
					}
				}
				ir, err := TranslateToSlurmJobIR(cl, dra.DefaultRegistry(), api, ctx, pod)
				if err != nil {
					t.Fatal(err)
				}
				wantPods, wantCode := 1, fwk.Error
				if ready {
					wantPods, wantCode = 2, fwk.Success
				}
				if ir.RootPOM.TypeMeta != api.PodGroupTypeMeta || len(ir.Pods.Items) != wantPods || ptr.Deref(ir.JobInfo.MinNodes, 0) != int32(wantPods) {
					t.Errorf("gang allocation changed: root=%v pods=%d minNodes=%v", ir.RootPOM.TypeMeta, len(ir.Pods.Items), ir.JobInfo.MinNodes)
				}
				if status := PreFilter(cl, dra.DefaultRegistry(), api, ctx, pod, ir); status.Code() != wantCode {
					t.Errorf("PreFilter = %v, want code %v", status, wantCode)
				}
			}
		})
	}
}
