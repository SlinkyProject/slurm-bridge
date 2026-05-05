// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/SlinkyProject/slurm-bridge/test"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

func getFeatures(install bool, test bool, beforeSteps []types.Feature) []types.Feature {
	steps := beforeSteps

	if install {
		steps = append(steps, installSlurmBridge())
	}
	if test {
		steps = append(steps, testSlurmCluster())
		steps = append(steps, testSlurmBridgeJobScheduling())
		steps = append(steps, testSlurmBridgePodScheduling())
	}

	return steps
}

func testSlurmCluster() types.Feature {
	return features.New("Validate the Slurm Cluster").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			return ctx
		}).
		Assess("controller is healthy", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crclient, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			podList := &corev1.PodList{}
			err = crclient.List(ctx, podList,
				client.InNamespace("slurm"),
				client.MatchingLabels{
					"app": "slurm-controller",
				},
			)

			if err != nil {
				t.Fatalf("failed to find valid slurm-controller")
			}
			checkControllerHealth(crclient, ctx, t, config)
			return ctx
		}).
		Assess("restapi is healthy", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crclient, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			podList := &corev1.PodList{}
			err = crclient.List(ctx, podList,
				client.InNamespace("slurm"),
				client.MatchingLabels{
					"app": "slurm-restapi",
				},
			)

			if err != nil {
				t.Fatalf("failed to find valid slurm-restapi")
			}
			checkRestAPIHealth(crclient, ctx, t, config)
			return ctx
		}).
		Feature()
}

func testSlurmBridgeJobScheduling() types.Feature {
	jobName := "job-single-e2e"
	return features.New("Slurm-scheduled job").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: test.SlurmBridgeNamespace,
				},
				Spec: batchv1.JobSpec{
					Completions: new(int32(1)),
					Parallelism: new(int32(1)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							SchedulerName: "slurm-bridge-scheduler",
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:    "job-single-e2e",
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
			crclient, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := crclient.Create(ctx, job); err != nil {
				t.Fatalf("failed to create job: %v", err)
			}
			return ctx
		}).
		Assess("job pod has slurm job ID label", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crclient, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			// Wait for the scheduler to assign a Slurm job ID to the pod.
			if err := wait.For(func(ctx context.Context) (bool, error) {
				podList := &corev1.PodList{}
				if err := crclient.List(ctx, podList,
					client.InNamespace(test.SlurmBridgeNamespace),
					client.MatchingLabels{"job-name": jobName},
				); err != nil {
					return false, err
				}
				if len(podList.Items) == 0 {
					return false, nil
				}
				pod := podList.Items[0]
				_, hasJobID := pod.Labels["scheduler.slinky.slurm.net/slurm-jobid"]
				return hasJobID, nil
			}, wait.WithTimeout(1*time.Minute), wait.WithInterval(10*time.Second)); err != nil {
				t.Fatalf("pod never received slurm-jobid label: %v", err)
			}
			return ctx
		}).
		Assess("job pod runs on slurm-bridge worker node", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crclient, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			var pod corev1.Pod
			if err := wait.For(func(ctx context.Context) (bool, error) {
				podList := &corev1.PodList{}
				if err := crclient.List(ctx, podList,
					client.InNamespace(test.SlurmBridgeNamespace),
					client.MatchingLabels{"job-name": jobName},
				); err != nil {
					return false, err
				}
				if len(podList.Items) == 0 {
					return false, nil
				}
				pod = podList.Items[0]
				return pod.Spec.NodeName != "", nil
			}, wait.WithTimeout(1*time.Minute), wait.WithInterval(10*time.Second)); err != nil {
				t.Fatalf("job pod was never scheduled to a node: %v", err)
			}

			node := &corev1.Node{}
			if err := crclient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
				t.Fatalf("failed to get node %s: %v", pod.Spec.NodeName, err)
			}
			if node.Labels["scheduler.slinky.slurm.net/slurm-bridge"] != "worker" {
				t.Fatalf("pod ran on node %s which is not a slurm-bridge worker (labels: %v)", pod.Spec.NodeName, node.Labels)
			}
			if pod.Spec.SchedulerName != "slurm-bridge-scheduler" {
				t.Fatalf("pod was not scheduled by Slurm-bridge scheduler")
			}
			return ctx
		}).
		Feature()
}

func testSlurmBridgePodScheduling() types.Feature {
	podName := "pod-single-e2e"
	return features.New("Slurm-scheduled pod").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: test.SlurmBridgeNamespace,
				},
				Spec: corev1.PodSpec{
					SchedulerName: "slurm-bridge-scheduler",
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
			crclient, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := crclient.Create(ctx, pod); err != nil {
				t.Fatalf("failed to create pod: %v", err)
			}
			return ctx
		}).
		Assess("pod runs on slurm-bridge worker node", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crclient, err := test.GetControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			pod := &corev1.Pod{}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := crclient.Get(ctx, client.ObjectKey{Namespace: test.SlurmBridgeNamespace, Name: podName}, pod); err != nil {
					return false, err
				}
				return pod.Spec.NodeName != "", nil
			}, wait.WithTimeout(1*time.Minute), wait.WithInterval(10*time.Second)); err != nil {
				t.Fatalf("pod was never scheduled to a node: %v", err)
			}

			node := &corev1.Node{}
			if err := crclient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
				t.Fatalf("failed to get node %s: %v", pod.Spec.NodeName, err)
			}
			if node.Labels["scheduler.slinky.slurm.net/slurm-bridge"] != "worker" {
				t.Fatalf("pod ran on node %s which is not a slurm-bridge worker (labels: %v)", pod.Spec.NodeName, node.Labels)
			}
			if pod.Spec.SchedulerName != "slurm-bridge-scheduler" {
				t.Fatalf("pod was not scheduled by Slurm-bridge scheduler")
			}
			return ctx
		}).
		Feature()
}
