// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func testAdmissionRoutingBoundaries() types.Feature {
	namespaceName := envconf.RandomName("e2e-routing", 40)
	managedPod := slurmTestPod(slurmBridgeNamespace,
		envconf.RandomName("admission-managed", 40), []string{"sh", "-c", "sleep 300"})
	managedPod.Spec.SchedulerName = corev1.DefaultSchedulerName
	explicitPod := slurmTestPod(namespaceName,
		envconf.RandomName("admission-explicit", 40), []string{"sh", "-c", "sleep 300"})
	controlPod := slurmTestPod(namespaceName,
		envconf.RandomName("admission-control", 40), []string{"sh", "-c", "sleep 300"})
	controlPod.Spec.SchedulerName = corev1.DefaultSchedulerName

	return features.New("Admission routing boundaries").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: namespaceName},
			}); err != nil {
				t.Fatalf("create unmanaged namespace: %v", err)
			}
			for _, pod := range []*corev1.Pod{managedPod, explicitPod, controlPod} {
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create pod %s/%s: %v", pod.Namespace, pod.Name, err)
				}
			}
			return ctx
		}).
		Assess("managed and explicit pods use Slurm while the control pod does not", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			for _, pod := range []*corev1.Pod{managedPod, explicitPod} {
				observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), podHasSlurmAllocation)
				if err != nil {
					t.Fatalf("pod %s/%s was not routed through Slurm: %v", pod.Namespace, pod.Name, err)
				}
				assertBridgePod(t, ctx, crClient, observed)
			}
			observedControl, err := waitForPod(ctx, crClient,
				client.ObjectKeyFromObject(controlPod),
				func(pod *corev1.Pod) bool { return pod.Status.Phase == corev1.PodRunning })
			if err != nil {
				t.Fatalf("control pod did not run with the default scheduler: %v", err)
			}
			controlPod = observedControl
			if controlPod.Spec.SchedulerName != corev1.DefaultSchedulerName {
				t.Errorf("control pod scheduler is %q, want %q",
					controlPod.Spec.SchedulerName, corev1.DefaultSchedulerName)
			}
			if controlPod.Labels[slurmJobIDLabel] != "" {
				t.Errorf("control pod unexpectedly has Slurm job ID %s", controlPod.Labels[slurmJobIDLabel])
			}
			node := &corev1.Node{}
			if err := crClient.Get(ctx, client.ObjectKey{Name: controlPod.Spec.NodeName}, node); err != nil {
				t.Fatalf("get control pod node: %v", err)
			}
			if node.Labels[slurmBridgeWorkerLabel] == "worker" {
				t.Errorf("default-scheduled control pod ran on managed bridge node %s", node.Name)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "admission routing boundaries",
				slurmBridgeNamespace, namespaceName, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, managedPod)
			deletePodAndAssertCleanup(ctx, t, config, crClient, explicitPod)
			deleteObject(t, ctx, crClient, controlPod)
			deleteObject(t, ctx, crClient, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			return ctx
		}).
		Feature()
}
