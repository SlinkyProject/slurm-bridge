// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func testSlurmJobRoundTrip() types.Feature {
	podName := envconf.RandomName("roundtrip-pod-e2e", 40)
	jobName := envconf.RandomName("roundtrip-job-e2e", 40)
	pod := slurmTestPod(slurmBridgeNamespace, podName, []string{"sh", "-c", "sleep 300"})
	pod.Annotations = map[string]string{
		wellknown.AnnotationJobName:   jobName,
		wellknown.AnnotationPartition: slurmBridgePartition,
		wellknown.AnnotationTimeLimit: "5",
		wellknown.AnnotationMinNodes:  "1",
		wellknown.AnnotationMaxNodes:  "1",
		wellknown.AnnotationExclusive: "true",
	}
	pod.Spec.Containers[0].Resources = slurmTestResources("2", "200Mi")

	return features.New("Slurm annotation and resource round trip").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create round-trip pod: %v", err)
			}
			return ctx
		}).
		Assess("Slurm job matches Kubernetes annotations and resources", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("round-trip pod was not allocated: %v", err)
			}
			pod = observed
			output, err := querySlurmJob(ctx, config, crClient, pod.Labels[slurmJobIDLabel])
			if err != nil {
				t.Fatal(err)
			}
			wantFields := map[string]string{
				"JobName":       jobName,
				"Partition":     slurmBridgePartition,
				"TimeLimit":     "00:05:00",
				"NumNodes":      "1",
				"CPUs/Task":     "2",
				"MinMemoryNode": "200M",
				"OverSubscribe": "NO",
			}
			for field, want := range wantFields {
				got, err := slurmJobField(output, field)
				if err != nil {
					t.Error(err)
					continue
				}
				if got != want {
					t.Errorf("Slurm %s=%q, want %q", field, got, want)
				}
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Slurm annotation and resource round trip",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			return ctx
		}).
		Feature()
}
