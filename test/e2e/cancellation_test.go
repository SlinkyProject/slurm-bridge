// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func testKubernetesCancellation() types.Feature {
	podName := envconf.RandomName("cancel-kubernetes-e2e", 40)
	pod := slurmTestPod(slurmBridgeNamespace, podName, []string{"sh", "-c", "sleep 300"})
	deleted := false

	return features.New("Kubernetes to Slurm cancellation").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create cancellation pod: %v", err)
			}
			return ctx
		}).
		Assess("deleting the pod cancels and removes its Slurm job", func(
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
				t.Fatalf("cancellation pod was not allocated: %v", err)
			}
			pod = observed
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			deleted = true
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Kubernetes to Slurm cancellation",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if deleted || !e2eCleanupEnabled(t) {
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

func testSlurmCancellation() types.Feature {
	podName := envconf.RandomName("cancel-slurm-e2e", 40)
	pod := slurmTestPod(slurmBridgeNamespace, podName, []string{"sh", "-c", "sleep 300"})
	deleted := false

	return features.New("Slurm to Kubernetes cancellation").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create cancellation pod: %v", err)
			}
			return ctx
		}).
		Assess("canceling the Slurm job terminates the pod", func(
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
				t.Fatalf("cancellation pod was not allocated: %v", err)
			}
			pod = observed
			jobID := pod.Labels[slurmJobIDLabel]
			controllerPod, err := getSlurmControllerPod(ctx, crClient)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execInPod(ctx, config, controllerPod, "scancel", jobID); err != nil {
				t.Fatalf("cancel Slurm job %s: %v", jobID, err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})
				return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
			}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
				t.Fatalf("pod was not deleted after Slurm cancellation: %v", err)
			}
			deleted = true
			if err := waitForSlurmJobGone(ctx, config, crClient, jobID); err != nil {
				t.Errorf("Slurm job %s remained queued after cancellation: %v", jobID, err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Slurm to Kubernetes cancellation",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if deleted || !e2eCleanupEnabled(t) {
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
