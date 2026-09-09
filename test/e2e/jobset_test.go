// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
)

func testSlurmBridgeJobSetScheduling() types.Feature {
	jobSetName := envconf.RandomName("jobset-e2e", 40)
	var slurmJobIDs []string
	jobSet := &jobsetv1alpha2.JobSet{
		ObjectMeta: metav1.ObjectMeta{Name: jobSetName, Namespace: slurmBridgeNamespace},
		Spec: jobsetv1alpha2.JobSetSpec{ReplicatedJobs: []jobsetv1alpha2.ReplicatedJob{{
			Name:     "workers",
			Replicas: 2,
			Template: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				Parallelism:  ptr.To[int32](1),
				Completions:  ptr.To[int32](1),
				BackoffLimit: ptr.To[int32](0),
				Template:     slurmTestPodTemplate([]string{"sh", "-c", "sleep 5"}),
			}},
		}}},
	}

	return features.New("JobSet workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, jobSet); err != nil {
				t.Fatalf("create JobSet: %v", err)
			}
			return ctx
		}).
		Assess("replicated jobs run through Slurm", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{jobsetv1alpha2.JobSetNameKey: jobSetName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("JobSet pods were not allocated: %v", err)
			}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
			}
			slurmJobIDs = podSlurmJobIDs(pods)
			if len(slurmJobIDs) != 2 {
				t.Errorf("JobSet has %d distinct Slurm jobs, want 2: %v", len(slurmJobIDs), slurmJobIDs)
			}
			return ctx
		}).
		Assess("JobSet completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{jobsetv1alpha2.JobSetNameKey: jobSetName}, 2, podFinishedAndReleased); err != nil {
				t.Fatalf("JobSet did not complete finalizer processing: %v", err)
			}
			assertSlurmJobsGone(ctx, t, config, crClient, slurmJobIDs)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "JobSet workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "jobset-system")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deleteObject(t, ctx, crClient, jobSet)
			return ctx
		}).
		Feature()
}

func testSlurmBridgeJobSetPodGroupScheduling() types.Feature {
	workloadName := envconf.RandomName("jobset-workload-e2e", 40)
	jobSetName := envconf.RandomName("jobset-podgroup-e2e", 40)
	podGroupName := jobSetName + "-workers"
	var slurmJobIDs []string
	policy := schedulingv1alpha2.PodGroupSchedulingPolicy{
		Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
	}
	workload := &schedulingv1alpha2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: slurmBridgeNamespace},
		Spec: schedulingv1alpha2.WorkloadSpec{
			ControllerRef: &schedulingv1alpha2.TypedLocalObjectReference{
				APIGroup: jobsetv1alpha2.GroupVersion.Group, Kind: "JobSet", Name: jobSetName,
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
	jobSet := &jobsetv1alpha2.JobSet{
		ObjectMeta: metav1.ObjectMeta{Name: jobSetName, Namespace: slurmBridgeNamespace},
		Spec: jobsetv1alpha2.JobSetSpec{ReplicatedJobs: []jobsetv1alpha2.ReplicatedJob{{
			Name:     "workers",
			Replicas: 2,
			Template: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				Parallelism:  ptr.To[int32](1),
				Completions:  ptr.To[int32](1),
				BackoffLimit: ptr.To[int32](0),
				Template:     template,
			}},
		}}},
	}

	return features.New("JobSet native PodGroup workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			skipKubernetesPodGroupOnUnsupportedVersion(t, config)
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			for _, object := range []client.Object{workload, podGroup, jobSet} {
				if err := crClient.Create(ctx, object); err != nil {
					t.Fatalf("create %T %s: %v", object, object.GetName(), err)
				}
			}
			return ctx
		}).
		Assess("JobSet gang shares one two-node Slurm job", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{jobsetv1alpha2.JobSetNameKey: jobSetName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("JobSet PodGroup pods were not allocated: %v", err)
			}
			slurmJobIDs = podSlurmJobIDs(pods)
			if len(slurmJobIDs) != 1 {
				t.Fatalf("JobSet PodGroup pods have %d Slurm jobs, want 1: %v", len(slurmJobIDs), slurmJobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, slurmJobIDs[0], 2)
			nodes := map[string]struct{}{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				nodes[pods[i].Spec.NodeName] = struct{}{}
			}
			if len(nodes) != 2 {
				t.Errorf("JobSet PodGroup pods use %d nodes, want 2: %v", len(nodes), nodes)
			}
			assertKubernetesPodGroupScheduled(ctx, t, crClient, podGroup)
			return ctx
		}).
		Assess("JobSet gang completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{jobsetv1alpha2.JobSetNameKey: jobSetName}, 2, podFinishedAndReleased); err != nil {
				t.Fatalf("JobSet gang did not complete finalizer processing: %v", err)
			}
			assertSlurmJobsGone(ctx, t, config, crClient, slurmJobIDs)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "JobSet native PodGroup workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "jobset-system")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			for _, object := range []client.Object{jobSet, podGroup, workload} {
				deleteObject(t, ctx, crClient, object)
			}
			return ctx
		}).
		Feature()
}
