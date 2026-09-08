// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
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

func testLeaderWorkerSetPodGroupScheduling() types.Feature {
	workloadName := envconf.RandomName("lws-workload-e2e", 40)
	lwsName := envconf.RandomName("lws-podgroup-e2e", 40)
	podGroupName := lwsName + "-workers"
	policy := schedulingv1alpha2.PodGroupSchedulingPolicy{
		Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
	}
	workload := &schedulingv1alpha2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: slurmBridgeNamespace},
		Spec: schedulingv1alpha2.WorkloadSpec{
			ControllerRef: &schedulingv1alpha2.TypedLocalObjectReference{
				APIGroup: lwsv1.GroupVersion.Group, Kind: "LeaderWorkerSet", Name: lwsName,
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
	leaderTemplate := slurmTestPodTemplate([]string{"sh", "-c", "sleep 300"})
	workerTemplate := slurmTestPodTemplate([]string{"sh", "-c", "sleep 300"})
	for _, template := range []*corev1.PodTemplateSpec{&leaderTemplate, &workerTemplate} {
		template.Spec.RestartPolicy = corev1.RestartPolicyAlways
		template.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(podGroupName)}
	}
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

	return features.New("LeaderWorkerSet native PodGroup workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			skipKubernetesPodGroupOnUnsupportedVersion(t, config)
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			for _, object := range []client.Object{workload, podGroup, lws} {
				if err := crClient.Create(ctx, object); err != nil {
					t.Fatalf("create %T %s: %v", object, object.GetName(), err)
				}
			}
			return ctx
		}).
		Assess("leader and worker share the PodGroup Slurm allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{lwsv1.SetNameLabelKey: lwsName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("LeaderWorkerSet PodGroup pods were not allocated: %v", err)
			}
			jobIDs := podSlurmJobIDs(pods)
			if len(jobIDs) != 1 {
				t.Fatalf("LeaderWorkerSet PodGroup pods have %d Slurm jobs, want 1: %v", len(jobIDs), jobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, jobIDs[0], 2)
			nodes := map[string]struct{}{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				nodes[pods[i].Spec.NodeName] = struct{}{}
			}
			if len(nodes) != 2 {
				t.Errorf("LeaderWorkerSet PodGroup uses %d nodes, want 2: %v", len(nodes), nodes)
			}
			assertKubernetesPodGroupScheduled(ctx, t, crClient, podGroup)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "LeaderWorkerSet native PodGroup workload",
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
				t.Errorf("list LeaderWorkerSet PodGroup pods for cleanup: %v", err)
			}
			deleteObject(t, ctx, crClient, lws)
			pods := make([]*corev1.Pod, 0, len(podList.Items))
			for i := range podList.Items {
				pods = append(pods, &podList.Items[i])
			}
			deletePodsAndAssertCleanup(ctx, t, config, crClient, pods...)
			for _, object := range []client.Object{podGroup, workload} {
				deleteObject(t, ctx, crClient, object)
			}
			return ctx
		}).
		Feature()
}
