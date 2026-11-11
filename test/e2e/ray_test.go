// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func testRayPodGroupScheduling() types.Feature { return testRayScheduling(false) }

func TestRayTPUAlias(t *testing.T) {
	requireE2ESuite(t, "tpu", true)
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseE2ECleanupFromEnvironment(); err != nil {
		t.Fatal(err)
	}
	testEnv.Test(t, withSuiteLabel(testRayScheduling(true), "tpu"))
}

func testRayScheduling(tpu bool) types.Feature {
	const label = "test.slinky.slurm.net/ray-job"
	job := newRayPodGroupJob(envconf.RandomName("ray-podgroup-e2e", 32), label)
	name := "RayJob generated native PodGroup scheduling"
	var aliasClass *resourcev1.DeviceClass
	if tpu {
		name = "RayJob TPU alias allocation"
		configureRayTPUJob(job)
	}
	var api kubernetesPodGroupAPI
	var slurmJobIDs []string

	return features.New(name).
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
			if tpu {
				aliasClass = getTPUAliasClass(ctx, t, c)
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
			if tpu {
				assertRayTPUAlias(ctx, t, c, job, pods, aliasClass.Name)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, name,
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
			if tpu {
				assertRayTPUClaimsGone(ctx, t, c, label, job.GetName())
			}
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

func configureRayTPUJob(job *unstructured.Unstructured) {
	workers, _, _ := unstructured.NestedSlice(job.Object, "spec", "rayClusterSpec", "workerGroupSpecs")
	worker := workers[0].(map[string]any)
	worker["numOfHosts"] = int64(1)
	spec := worker["template"].(map[string]any)["spec"].(map[string]any)
	spec["tolerations"] = []any{map[string]any{"key": "google.com/tpu", "operator": "Exists", "effect": "NoSchedule"}}
	container := spec["containers"].([]any)[0].(map[string]any)
	for _, key := range []string{"requests", "limits"} {
		container["resources"].(map[string]any)[key].(map[string]any)["google.com/tpu"] = "4"
	}
	_ = unstructured.SetNestedSlice(job.Object, workers, "spec", "rayClusterSpec", "workerGroupSpecs")
	_ = unstructured.SetNestedField(job.Object, `python -c '
import os, stat, ray
ray.init()
@ray.remote(resources={"TPU": 4})
def check():
    assert ray.cluster_resources().get("TPU") == 4, ray.cluster_resources()
    assert all(stat.S_ISBLK(os.stat("/dev/accel%d" % i).st_mode) for i in range(4))
    assert os.environ["TPU_WORKER_ID"] == "0"
    return "TPU_ALIAS_OK"
print(ray.get(check.remote(), timeout=180), flush=True)
'`, "spec", "entrypoint")
}

func assertRayTPUAlias(ctx context.Context, t *testing.T, c client.Client, job *unstructured.Unstructured, pods []corev1.Pod, className string) {
	t.Helper()
	var worker *corev1.Pod
	for i := range pods {
		if pods[i].Labels["ray.io/node-type"] == "worker" {
			worker = &pods[i]
		}
	}
	if worker == nil {
		t.Fatal("missing Ray worker")
	}
	assertTPUAliasClaim(ctx, t, c, worker, className)
	if err := wait.For(func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKeyFromObject(job), job); err != nil {
			return false, err
		}
		status, _, _ := unstructured.NestedString(job.Object, "status", "jobStatus")
		if status == "FAILED" {
			return false, fmt.Errorf("Ray task failed: %s", statusJSON(job.Object["status"]))
		}
		return status == "SUCCEEDED", nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(2*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func assertRayTPUClaimsGone(ctx context.Context, t *testing.T, c client.Client, label, jobName string) {
	t.Helper()
	if err := wait.For(func(ctx context.Context) (bool, error) {
		pods := &corev1.PodList{}
		if err := c.List(ctx, pods, client.InNamespace(slurmBridgeNamespace), client.MatchingLabels{label: jobName}); err != nil {
			return false, err
		}
		claims := &resourcev1.ResourceClaimList{}
		if err := c.List(ctx, claims, client.InNamespace(slurmBridgeNamespace)); err != nil {
			return false, err
		}
		for _, claim := range claims.Items {
			for _, owner := range claim.OwnerReferences {
				if owner.Kind == "Pod" && strings.HasPrefix(owner.Name, jobName) {
					return false, nil
				}
			}
		}
		return len(pods.Items) == 0, nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(2*time.Second)); err != nil {
		t.Errorf("Ray TPU cleanup: %v", err)
	}
}
