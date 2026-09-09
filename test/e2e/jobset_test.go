// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
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
