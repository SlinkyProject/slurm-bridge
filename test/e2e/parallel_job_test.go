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
)

func testSlurmBridgeParallelJobScheduling() types.Feature {
	jobName := envconf.RandomName("job-parallel-e2e", 40)
	var slurmJobIDs []string
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: slurmBridgeNamespace},
		Spec: batchv1.JobSpec{
			Parallelism:  ptr.To[int32](3),
			Completions:  ptr.To[int32](3),
			BackoffLimit: ptr.To[int32](0),
			Template:     slurmTestPodTemplate([]string{"sh", "-c", "sleep 5"}),
		},
	}

	return features.New("Parallel Kubernetes Job").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, job); err != nil {
				t.Fatalf("create parallel Job: %v", err)
			}
			return ctx
		}).
		Assess("all parallel pods receive independent Slurm allocations", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 3, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("parallel Job pods were not allocated: %v", err)
			}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
			}
			slurmJobIDs = podSlurmJobIDs(pods)
			if len(slurmJobIDs) != 3 {
				t.Errorf("parallel Job has %d distinct Slurm jobs, want 3: %v", len(slurmJobIDs), slurmJobIDs)
			}
			return ctx
		}).
		Assess("parallel Job completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 3, podFinishedAndReleased); err != nil {
				t.Fatalf("parallel Job did not complete finalizer processing: %v", err)
			}
			assertSlurmJobsGone(ctx, t, config, crClient, slurmJobIDs)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "parallel Kubernetes Job",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deleteObject(t, ctx, crClient, job)
			return ctx
		}).
		Feature()
}
