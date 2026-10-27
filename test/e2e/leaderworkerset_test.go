// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
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
			Replicas:      ptr.To[int32](2),
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
		Assess("each replica shares its own two-node Slurm job", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{lwsv1.SetNameLabelKey: lwsName}, 4, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("LeaderWorkerSet pods were not allocated: %v", err)
			}
			jobIDs := podSlurmJobIDs(pods)
			if len(jobIDs) != 2 {
				t.Fatalf("LeaderWorkerSet replicas have %d Slurm jobs, want 2: %v", len(jobIDs), jobIDs)
			}
			groups := map[string][]corev1.Pod{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				index := pods[i].Labels[lwsv1.GroupIndexLabelKey]
				groups[index] = append(groups[index], pods[i])
			}
			for _, index := range []string{"0", "1"} {
				members := groups[index]
				if len(members) != 2 {
					t.Fatalf("LeaderWorkerSet replica %s has %d Pods, want 2", index, len(members))
				}
				groupJobIDs := podSlurmJobIDs(members)
				if len(groupJobIDs) != 1 {
					t.Fatalf("LeaderWorkerSet replica %s has %d Slurm jobs, want 1: %v", index, len(groupJobIDs), groupJobIDs)
				}
				assertSlurmNodeCount(ctx, t, config, crClient, groupJobIDs[0], 2)
				nodes := map[string]struct{}{}
				for i := range members {
					nodes[members[i].Spec.NodeName] = struct{}{}
				}
				if len(nodes) != 2 {
					t.Errorf("LeaderWorkerSet replica %s uses %d nodes, want 2: %v", index, len(nodes), nodes)
				}
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

func testLeaderWorkerSetWorkerFailure() types.Feature {
	lwsName := envconf.RandomName("lws-failure-e2e", 40)
	leaderTemplate := slurmTestPodTemplate([]string{"sh", "-c", "sleep 300"})
	// The worker exits once the test creates /tmp/fail, which LWS treats as a
	// pod restart.
	workerTemplate := slurmTestPodTemplate([]string{"sh", "-c",
		"while [ ! -e /tmp/fail ]; do sleep 1; done; exit 1"})
	leaderTemplate.Spec.RestartPolicy = corev1.RestartPolicyAlways
	workerTemplate.Spec.RestartPolicy = corev1.RestartPolicyAlways
	lws := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Name: lwsName, Namespace: slurmBridgeNamespace},
		Spec: lwsv1.LeaderWorkerSetSpec{
			Replicas: ptr.To[int32](1),
			// Keep the default Sequential termination policy: LWS deletes the leader
			// before its workers, so recovery depends on canceling the job once the
			// leader stops.
			StartupPolicy: lwsv1.LeaderCreatedStartupPolicy,
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size:           ptr.To[int32](2),
				RestartPolicy:  lwsv1.RecreateGroupOnPodRestart,
				LeaderTemplate: &leaderTemplate,
				WorkerTemplate: workerTemplate,
			},
		},
	}

	return features.New("LeaderWorkerSet worker failure").
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
		Assess("a failed worker cancels the group's Slurm job and the recreated group gets a new one", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
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
				t.Fatalf("LeaderWorkerSet group has %d Slurm jobs, want 1: %v", len(jobIDs), jobIDs)
			}
			oldJobID := jobIDs[0]
			oldPods := map[k8stypes.UID]struct{}{}
			var worker *corev1.Pod
			for i := range pods {
				oldPods[pods[i].UID] = struct{}{}
				if pods[i].Labels[lwsv1.WorkerIndexLabelKey] != "0" {
					worker = &pods[i]
				}
			}
			if worker == nil {
				t.Fatalf("LeaderWorkerSet group has no worker pod")
			}
			// Pods are labeled with their Slurm job before their containers start.
			worker, err = waitForPod(ctx, crClient, client.ObjectKeyFromObject(worker), func(pod *corev1.Pod) bool {
				return slices.ContainsFunc(pod.Status.ContainerStatuses, func(status corev1.ContainerStatus) bool {
					return status.State.Running != nil
				})
			})
			if err != nil {
				t.Fatalf("worker container did not start: %v", err)
			}

			if _, err := execInPod(ctx, config, worker, "touch", "/tmp/fail"); err != nil {
				t.Fatalf("fail worker %s: %v", worker.Name, err)
			}

			if err := waitForSlurmJobGone(ctx, config, crClient, oldJobID); err != nil {
				t.Fatalf("Slurm job %s remained active after the worker failed: %v", oldJobID, err)
			}
			replacements, err := waitForReplacementLWSGroup(ctx, crClient, lwsName, oldPods, 2)
			if err != nil {
				t.Fatalf("LeaderWorkerSet group was not rescheduled: %v", err)
			}
			newJobIDs := podSlurmJobIDs(replacements)
			if len(newJobIDs) != 1 {
				t.Fatalf("recreated group has %d Slurm jobs, want 1: %v", len(newJobIDs), newJobIDs)
			}
			if newJobIDs[0] == oldJobID {
				t.Fatalf("recreated group reused Slurm job %s", oldJobID)
			}
			for i := range replacements {
				assertBridgePod(t, ctx, crClient, &replacements[i])
			}
			assertSlurmNodeCount(ctx, t, config, crClient, newJobIDs[0], 2)

			// Old pods must release their finalizer once their job is gone.
			if err := wait.For(func(ctx context.Context) (bool, error) {
				podList := &corev1.PodList{}
				if err := crClient.List(ctx, podList, client.InNamespace(slurmBridgeNamespace),
					client.MatchingLabels{lwsv1.SetNameLabelKey: lwsName}); err != nil {
					return false, err
				}
				for i := range podList.Items {
					if _, ok := oldPods[podList.Items[i].UID]; ok {
						return false, nil
					}
				}
				return true, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
				t.Errorf("pods from the failed group were not deleted: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "LeaderWorkerSet worker failure",
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

// waitForReplacementLWSGroup waits for count allocated pods in the set that are
// not in oldPods. Pods from the failed group are ignored, since they can stay
// terminal until their Slurm cleanup finishes.
func waitForReplacementLWSGroup(
	ctx context.Context,
	crClient client.Client,
	lwsName string,
	oldPods map[k8stypes.UID]struct{},
	count int,
) ([]corev1.Pod, error) {
	var pods []corev1.Pod
	err := wait.For(func(ctx context.Context) (bool, error) {
		podList := &corev1.PodList{}
		if err := crClient.List(ctx, podList, client.InNamespace(slurmBridgeNamespace),
			client.MatchingLabels{lwsv1.SetNameLabelKey: lwsName}); err != nil {
			return false, err
		}
		pods = pods[:0]
		for i := range podList.Items {
			pod := &podList.Items[i]
			if _, ok := oldPods[pod.UID]; ok || pod.DeletionTimestamp != nil {
				continue
			}
			if pod.Status.Phase == corev1.PodFailed {
				return false, fmt.Errorf("pod %s failed: %s", pod.Name, pod.Status.Message)
			}
			pods = append(pods, *pod)
		}
		if len(pods) != count {
			return false, nil
		}
		for i := range pods {
			if !podHasSlurmAllocation(&pods[i]) {
				return false, nil
			}
		}
		return true, nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(3*time.Second))
	return pods, err
}

func testLeaderWorkerSetPodGroupScheduling(api kubernetesPodGroupAPI) types.Feature {
	featureName := "LeaderWorkerSet native PodGroup workload (" + string(api) + ")"
	workloadName := envconf.RandomName("lws-workload-e2e", 40)
	lwsName := envconf.RandomName("lws-podgroup-e2e", 40)
	podGroupName := lwsName + "-workers"
	workload := newKubernetesWorkload(api, workloadName, lwsv1.GroupVersion.Group, "LeaderWorkerSet", lwsName)
	podGroup := newKubernetesPodGroup(api, podGroupName, workloadName)
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
			GroupIdentity: lwsv1.GroupIdentityOrdinal,
			StartupPolicy: lwsv1.LeaderCreatedStartupPolicy,
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size:           ptr.To[int32](2),
				RestartPolicy:  lwsv1.RecreateGroupOnPodRestart,
				LeaderTemplate: &leaderTemplate,
				WorkerTemplate: workerTemplate,
			},
		},
	}

	return features.New(featureName).
		WithLabel("workload-api", string(api)).
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireKubernetesPodGroupAPI(t, config, api)
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
			captureReleaseSignalDiagnostics(t, featureName,
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
