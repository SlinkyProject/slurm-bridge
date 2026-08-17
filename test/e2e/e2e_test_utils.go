// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

const (
	slurmBridgeNamespace   = "slurm-bridge"
	slurmBridgeScheduler   = "slurm-bridge-scheduler"
	slurmBridgeWorkerLabel = "scheduler.slinky.slurm.net/slurm-bridge"
	slurmJobIDLabel        = "scheduler.slinky.slurm.net/slurm-jobid"
)

func getControllerRuntimeClient(config *envconf.Config) (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	return client.New(config.Client().RESTConfig(), client.Options{Scheme: scheme})
}

func testSlurmBridgeJobScheduling() types.Feature {
	jobName := envconf.RandomName("job-single-e2e", 32)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: slurmBridgeNamespace,
		},
		Spec: batchv1.JobSpec{
			Completions: new(int32(1)),
			Parallelism: new(int32(1)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					SchedulerName: slurmBridgeScheduler,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    jobName,
							Image:   "busybox:stable",
							Command: []string{"sh", "-c", "sleep 3"},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("100Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	return features.New("Slurm-scheduled job").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := crClient.Create(ctx, job); err != nil {
				t.Fatalf("failed to create job: %v", err)
			}
			return ctx
		}).
		Assess("job pod has Slurm job ID label", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				podList := &corev1.PodList{}
				if err := crClient.List(ctx, podList,
					client.InNamespace(slurmBridgeNamespace),
					client.MatchingLabels{"job-name": jobName},
				); err != nil {
					return false, err
				}
				if len(podList.Items) == 0 {
					return false, nil
				}
				_, hasJobID := podList.Items[0].Labels[slurmJobIDLabel]
				return hasJobID, nil
			}, wait.WithContext(ctx), wait.WithTimeout(time.Minute), wait.WithInterval(10*time.Second)); err != nil {
				t.Fatalf("pod never received Slurm job ID label: %v", err)
			}
			return ctx
		}).
		Assess("job pod runs on Slurm Bridge worker node", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			var pod corev1.Pod
			if err := wait.For(func(ctx context.Context) (bool, error) {
				podList := &corev1.PodList{}
				if err := crClient.List(ctx, podList,
					client.InNamespace(slurmBridgeNamespace),
					client.MatchingLabels{"job-name": jobName},
				); err != nil {
					return false, err
				}
				if len(podList.Items) == 0 {
					return false, nil
				}
				pod = podList.Items[0]
				return pod.Spec.NodeName != "", nil
			}, wait.WithContext(ctx), wait.WithTimeout(time.Minute), wait.WithInterval(10*time.Second)); err != nil {
				t.Fatalf("job pod was never scheduled to a node: %v", err)
			}

			node := &corev1.Node{}
			if err := crClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
				t.Fatalf("failed to get node %s: %v", pod.Spec.NodeName, err)
			}
			if node.Labels[slurmBridgeWorkerLabel] != "worker" {
				t.Fatalf("pod ran on node %s which is not a Slurm Bridge worker (labels: %v)", pod.Spec.NodeName, node.Labels)
			}
			if pod.Spec.SchedulerName != slurmBridgeScheduler {
				t.Fatalf("pod was not scheduled by Slurm Bridge scheduler")
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for job cleanup: %v", err)
				return ctx
			}
			if err := crClient.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("failed to delete job %s: %v", jobName, err)
			}
			return ctx
		}).
		Feature()
}

func testSlurmBridgePodScheduling() types.Feature {
	podName := envconf.RandomName("pod-single-e2e", 32)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: slurmBridgeNamespace,
		},
		Spec: corev1.PodSpec{
			SchedulerName: slurmBridgeScheduler,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    podName,
					Image:   "busybox:stable",
					Command: []string{"sh", "-c", "sleep 100"},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("100Mi"),
						},
					},
				},
			},
		},
	}

	return features.New("Slurm-scheduled pod").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("failed to create pod: %v", err)
			}
			return ctx
		}).
		Assess("pod runs on Slurm Bridge worker node", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
					return false, err
				}
				return pod.Spec.NodeName != "", nil
			}, wait.WithContext(ctx), wait.WithTimeout(time.Minute), wait.WithInterval(10*time.Second)); err != nil {
				t.Fatalf("pod was never scheduled to a node: %v", err)
			}

			node := &corev1.Node{}
			if err := crClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
				t.Fatalf("failed to get node %s: %v", pod.Spec.NodeName, err)
			}
			if node.Labels[slurmBridgeWorkerLabel] != "worker" {
				t.Fatalf("pod ran on node %s which is not a Slurm Bridge worker (labels: %v)", pod.Spec.NodeName, node.Labels)
			}
			if pod.Spec.SchedulerName != slurmBridgeScheduler {
				t.Fatalf("pod was not scheduled by Slurm Bridge scheduler")
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for pod cleanup: %v", err)
				return ctx
			}
			if err := crClient.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("failed to delete pod %s: %v", podName, err)
			}
			return ctx
		}).
		Feature()
}
