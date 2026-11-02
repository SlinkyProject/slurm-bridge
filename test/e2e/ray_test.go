// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func testRayPodGroupScheduling() types.Feature {
	const label = "test.slinky.slurm.net/ray-job"
	job := newRayPodGroupJob(envconf.RandomName("ray-podgroup-e2e", 32), label)
	var api kubernetesPodGroupAPI
	var slurmJobIDs []string

	return features.New("RayJob generated native PodGroup scheduling").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			discoveryClient, err := discovery.NewDiscoveryClientForConfig(config.Client().RESTConfig())
			if err != nil {
				t.Fatalf("create discovery client: %v", err)
			}
			api, err = discoverKubernetesPodGroupAPI(discoveryClient)
			if err != nil {
				t.Fatal(err)
			}
			if api == "" {
				t.Skip("Ray native PodGroups require Kubernetes 1.36 or newer")
			}
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := c.Create(ctx, job); err != nil {
				t.Fatalf("create RayJob %s (requires KubeRay with KubernetesWAS enabled): %v", job.GetName(), err)
			}
			return ctx
		}).
		Assess("KubeRay creates a gang whose head and worker share a Slurm allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, c, slurmBridgeNamespace,
				map[string]string{label: job.GetName()}, 2, func(pod *corev1.Pod) bool {
					return podHasSlurmAllocation(pod) && pod.Status.Phase == corev1.PodRunning
				})
			if err != nil {
				t.Fatalf("Ray head and worker were not scheduled: %v", err)
			}
			slurmJobIDs = podSlurmJobIDs(pods)
			if len(slurmJobIDs) != 1 {
				t.Fatalf("Ray pods have Slurm jobs %v, want one shared allocation", slurmJobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, c, slurmJobIDs[0], 2)
			nodes := map[string]struct{}{}
			var groupName string
			for i := range pods {
				pod := &pods[i]
				assertBridgePod(t, ctx, c, pod)
				nodes[pod.Spec.NodeName] = struct{}{}
				if pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
					t.Fatalf("Ray pod %s has no native PodGroup reference", pod.Name)
				}
				name := *pod.Spec.SchedulingGroup.PodGroupName
				if groupName != "" && name != groupName {
					t.Fatalf("Ray pod %s references PodGroup %q, want %q", pod.Name, name, groupName)
				}
				groupName = name
			}
			if len(nodes) != 2 {
				t.Errorf("Ray pods use %d nodes, want 2: %v", len(nodes), nodes)
			}

			group := &unstructured.Unstructured{}
			group.SetAPIVersion("scheduling.k8s.io/" + string(api))
			group.SetKind("PodGroup")
			if err := c.Get(ctx, client.ObjectKey{Namespace: job.GetNamespace(), Name: groupName}, group); err != nil {
				t.Fatalf("get Ray PodGroup %s: %v", groupName, err)
			}
			assertKubernetesPodGroupScheduled(ctx, t, c, group)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "RayJob generated native PodGroup scheduling",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "kuberay-system")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for RayJob cleanup: %v", err)
				return ctx
			}
			deleteObject(t, ctx, c, job)
			assertSlurmJobsGone(ctx, t, config, c, slurmJobIDs)
			return ctx
		}).
		Feature()
}

func newRayPodGroupJob(name, label string) *unstructured.Unstructured {
	template := func(container, memory string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"labels": map[string]any{label: name}},
			"spec": map[string]any{"containers": []any{map[string]any{
				"name": container, "image": "rayproject/ray:2.52.0",
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "1", "memory": memory},
					"limits":   map[string]any{"cpu": "1", "memory": memory},
				},
			}}},
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ray.io/v1", "kind": "RayJob",
		"metadata": map[string]any{
			"name": name, "namespace": slurmBridgeNamespace,
			"labels": map[string]any{"ray.io/gang-scheduling-enabled": "true"},
		},
		"spec": map[string]any{
			"submissionMode": "HTTPMode", "entrypoint": "python -c 'print(42)'",
			"shutdownAfterJobFinishes": false,
			"rayClusterSpec": map[string]any{
				"rayVersion": "2.52.0",
				"headGroupSpec": map[string]any{
					"rayStartParams": map[string]any{"num-cpus": "1", "object-store-memory": "100000000"},
					"template":       template("ray-head", "3Gi"),
				},
				"workerGroupSpecs": []any{map[string]any{
					"groupName": "worker", "replicas": int64(1), "minReplicas": int64(1), "maxReplicas": int64(1),
					"rayStartParams": map[string]any{"num-cpus": "1", "object-store-memory": "100000000"},
					"template":       template("ray-worker", "1Gi"),
				}},
			},
		},
	}}
}
