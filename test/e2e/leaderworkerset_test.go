// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func testLeaderWorkerSetScheduling() types.Feature {
	lwsName := envconf.RandomName("lws-e2e", 40)
	leaderTemplate := slurmTestPodTemplate([]string{"sh", "-c", "sleep 300"})
	workerTemplate := slurmTestPodTemplate([]string{"sh", "-c", "sleep 300"})
	leaderTemplate.Spec.RestartPolicy = corev1.RestartPolicyAlways
	workerTemplate.Spec.RestartPolicy = corev1.RestartPolicyAlways
	lws := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Name: lwsName, Namespace: slurmBridgeNamespace},
		Spec: lwsv1.LeaderWorkerSetSpec{
			Replicas:      ptr.To[int32](1),
			StartupPolicy: lwsv1.LeaderCreatedStartupPolicy,
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size:           ptr.To[int32](2),
				RestartPolicy:  lwsv1.RecreateGroupOnPodRestart,
				LeaderTemplate: &leaderTemplate,
				WorkerTemplate: workerTemplate,
			},
		},
	}

	return features.New("LeaderWorkerSet workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, lws); err != nil {
				t.Fatalf("create LeaderWorkerSet: %v", err)
			}
			return ctx
		}).
		Assess("leader and worker share one two-node Slurm job", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{lwsv1.SetNameLabelKey: lwsName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("LeaderWorkerSet pods were not allocated: %v", err)
			}
			jobIDs := podSlurmJobIDs(pods)
			if len(jobIDs) != 1 {
				t.Fatalf("LeaderWorkerSet pods have %d Slurm jobs, want 1: %v", len(jobIDs), jobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, jobIDs[0], 2)
			nodes := map[string]struct{}{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				nodes[pods[i].Spec.NodeName] = struct{}{}
			}
			if len(nodes) != 2 {
				t.Errorf("LeaderWorkerSet group uses %d nodes, want 2: %v", len(nodes), nodes)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "LeaderWorkerSet workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "lws-system")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			podList := &corev1.PodList{}
			if err := crClient.List(ctx, podList, client.InNamespace(slurmBridgeNamespace),
				client.MatchingLabels{lwsv1.SetNameLabelKey: lwsName}); err != nil {
				t.Errorf("list LeaderWorkerSet pods for cleanup: %v", err)
			}
			deleteObject(t, ctx, crClient, lws)
			pods := make([]*corev1.Pod, 0, len(podList.Items))
			for i := range podList.Items {
				pods = append(pods, &podList.Items[i])
			}
			deletePodsAndAssertCleanup(ctx, t, config, crClient, pods...)
			return ctx
		}).
		Feature()
}
