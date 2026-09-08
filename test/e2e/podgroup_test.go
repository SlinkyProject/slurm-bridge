// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

func testKubernetesPodGroupScheduling() types.Feature {
	workloadName := envconf.RandomName("workload-e2e", 40)
	jobName := envconf.RandomName("podgroup-job-e2e", 40)
	podGroupName := jobName + "-workers"
	var slurmJobIDs []string
	policy := schedulingv1alpha2.PodGroupSchedulingPolicy{
		Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
	}
	workload := &schedulingv1alpha2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: slurmBridgeNamespace},
		Spec: schedulingv1alpha2.WorkloadSpec{
			ControllerRef: &schedulingv1alpha2.TypedLocalObjectReference{
				APIGroup: "batch", Kind: "Job", Name: jobName,
			},
			PodGroupTemplates: []schedulingv1alpha2.PodGroupTemplate{{
				Name: "workers", SchedulingPolicy: policy,
			}},
		},
	}
	podGroup := &schedulingv1alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: podGroupName, Namespace: slurmBridgeNamespace},
		Spec: schedulingv1alpha2.PodGroupSpec{
			PodGroupTemplateRef: &schedulingv1alpha2.PodGroupTemplateReference{
				Workload: &schedulingv1alpha2.WorkloadPodGroupTemplateReference{
					WorkloadName: workloadName, PodGroupTemplateName: "workers",
				},
			},
			SchedulingPolicy: policy,
		},
	}
	template := slurmTestPodTemplate([]string{"sh", "-c", "sleep 10"})
	template.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(podGroupName)}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: slurmBridgeNamespace},
		Spec: batchv1.JobSpec{
			Parallelism:  ptr.To[int32](2),
			Completions:  ptr.To[int32](2),
			BackoffLimit: ptr.To[int32](0),
			Template:     template,
		},
	}

	return features.New("Kubernetes 1.36 PodGroup workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			skipKubernetesPodGroupOnUnsupportedVersion(t, config)
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			for _, object := range []client.Object{workload, podGroup, job} {
				if err := crClient.Create(ctx, object); err != nil {
					t.Fatalf("create %T %s: %v", object, object.GetName(), err)
				}
			}
			return ctx
		}).
		Assess("gang pods share one two-node Slurm job", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("PodGroup pods were not allocated: %v", err)
			}
			slurmJobIDs = podSlurmJobIDs(pods)
			if len(slurmJobIDs) != 1 {
				t.Fatalf("PodGroup pods have %d Slurm jobs, want 1: %v", len(slurmJobIDs), slurmJobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, slurmJobIDs[0], 2)
			nodes := map[string]struct{}{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				nodes[pods[i].Spec.NodeName] = struct{}{}
			}
			if len(nodes) != 2 {
				t.Errorf("PodGroup pods use %d nodes, want 2: %v", len(nodes), nodes)
			}
			assertKubernetesPodGroupScheduled(ctx, t, crClient, podGroup)
			return ctx
		}).
		Assess("gang Job completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 2, podFinishedAndReleased); err != nil {
				t.Fatalf("PodGroup Job did not complete finalizer processing: %v", err)
			}
			assertSlurmJobsGone(ctx, t, config, crClient, slurmJobIDs)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Kubernetes 1.36 PodGroup workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			for _, object := range []client.Object{job, podGroup, workload} {
				deleteObject(t, ctx, crClient, object)
			}
			return ctx
		}).
		Feature()
}

func testSchedulerPluginsPodGroupScheduling() types.Feature {
	podGroupName := envconf.RandomName("coscheduling-e2e", 40)
	var slurmJobIDs []string
	podGroup := &schedv1alpha1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: podGroupName, Namespace: slurmBridgeNamespace},
		Spec: schedv1alpha1.PodGroupSpec{
			MinMember: 2,
			MinResources: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(testCPU),
				corev1.ResourceMemory: resource.MustParse(testMemory),
			},
		},
	}
	pods := []*corev1.Pod{
		slurmTestPod(slurmBridgeNamespace, podGroupName+"-0", []string{"sh", "-c", "sleep 10"}),
		slurmTestPod(slurmBridgeNamespace, podGroupName+"-1", []string{"sh", "-c", "sleep 10"}),
	}
	for _, pod := range pods {
		pod.Labels = map[string]string{schedv1alpha1.PodGroupLabel: podGroupName}
	}

	return features.New("Scheduler-plugins PodGroup workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, podGroup); err != nil {
				t.Fatalf("create scheduler-plugins PodGroup: %v", err)
			}
			for _, pod := range pods {
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create PodGroup pod %s: %v", pod.Name, err)
				}
			}
			return ctx
		}).
		Assess("coscheduled pods share one Slurm allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			observed, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{schedv1alpha1.PodGroupLabel: podGroupName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("scheduler-plugins PodGroup pods were not allocated: %v", err)
			}
			slurmJobIDs = podSlurmJobIDs(observed)
			if len(slurmJobIDs) != 1 {
				t.Fatalf("PodGroup pods have %d Slurm jobs, want 1: %v", len(slurmJobIDs), slurmJobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, slurmJobIDs[0], 2)
			for i := range observed {
				assertBridgePod(t, ctx, crClient, &observed[i])
			}
			return ctx
		}).
		Assess("coscheduled pods complete", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{schedv1alpha1.PodGroupLabel: podGroupName}, 2, podFinishedAndReleased); err != nil {
				t.Fatalf("scheduler-plugins PodGroup pods did not complete finalizer processing: %v", err)
			}
			assertSlurmJobsGone(ctx, t, config, crClient, slurmJobIDs)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "scheduler-plugins PodGroup workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "scheduler-plugins")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			for _, pod := range pods {
				deleteObject(t, ctx, crClient, pod)
			}
			deleteObject(t, ctx, crClient, podGroup)
			return ctx
		}).
		Feature()
}
