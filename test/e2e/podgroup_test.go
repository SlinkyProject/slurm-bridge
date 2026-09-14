// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

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
