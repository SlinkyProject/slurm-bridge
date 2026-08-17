// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/cpuset"
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
	draCPUResource         = "deviceclass.resource.kubernetes.io/dra.cpu"
	draGPUResource         = "deviceclass.resource.kubernetes.io/gpu.example.com"
)

var gpuDeviceEnvironment = regexp.MustCompile(`(?m)^GPU_DEVICE_[0-9]+=(gpu-[0-9]+)$`)

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

func execInPod(ctx context.Context, config *envconf.Config, pod *corev1.Pod, command ...string) (string, error) {
	restConfig := config.Client().RESTConfig()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", fmt.Errorf("create Kubernetes client: %w", err)
	}

	request := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: pod.Spec.Containers[0].Name,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restConfig, http.MethodPost, request.URL())
	if err != nil {
		return "", fmt.Errorf("create pod exec executor: %w", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return "", fmt.Errorf("exec %v in pod %s/%s: %w (stderr: %s)", command, pod.Namespace, pod.Name, err, stderr.String())
	}

	return stdout.String(), nil
}

func draCPUSetFromEnvironment(environment string) (cpuset.CPUSet, error) {
	var allocation string
	for _, line := range strings.Split(environment, "\n") {
		name, value, found := strings.Cut(line, "=")
		if !found || !strings.HasPrefix(name, "DRA_CPUSET_") {
			continue
		}
		if allocation != "" {
			return cpuset.New(), fmt.Errorf("found multiple DRA CPU allocations in container environment")
		}
		allocation = value
	}
	if allocation == "" {
		return cpuset.New(), fmt.Errorf("DRA_CPUSET environment variable was not injected")
	}

	allocated, err := cpuset.Parse(allocation)
	if err != nil {
		return cpuset.New(), fmt.Errorf("parse allocated CPU set %q: %w", allocation, err)
	}
	return allocated, nil
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
			if t.Failed() {
				captureFailureDiagnostics(t, "Slurm-scheduled job", slurmBridgeNamespace, "slurm")
			}
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
				t.Fatalf("pod was never scheduled to a node: %v; observed status: %s", err, statusJSON(pod.Status))
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
			if t.Failed() {
				captureFailureDiagnostics(t, "Slurm-scheduled pod", slurmBridgeNamespace, "slurm")
			}
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

func testSlurmBridgeDRAResourceScheduling() types.Feature {
	podName := envconf.RandomName("pod-dra-e2e", 32)
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
					Command: []string{"sh", "-c", "sleep 300"},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("100Mi"),
							draCPUResource:        resource.MustParse("1"),
							draGPUResource:        resource.MustParse("1"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("100Mi"),
							draCPUResource:        resource.MustParse("1"),
							draGPUResource:        resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	return features.New("DRA resources allocated to container").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("failed to create DRA pod: %v", err)
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
				if pod.Status.Phase == corev1.PodFailed {
					return false, fmt.Errorf("DRA pod failed: %s", pod.Status.Message)
				}
				return pod.Status.Phase == corev1.PodRunning, nil
			}, wait.WithContext(ctx), wait.WithTimeout(2*time.Minute), wait.WithInterval(5*time.Second)); err != nil {
				t.Fatalf("DRA pod never reached Running: %v; observed status: %s", err, statusJSON(pod.Status))
			}

			node := &corev1.Node{}
			if err := crClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
				t.Fatalf("failed to get node %s: %v", pod.Spec.NodeName, err)
			}
			if node.Labels[slurmBridgeWorkerLabel] != "worker" {
				t.Fatalf("DRA pod ran on node %s which is not a Slurm Bridge worker", pod.Spec.NodeName)
			}
			return ctx
		}).
		Assess("container CPU set matches DRA allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			environment, err := execInPod(ctx, config, pod, "env")
			if err != nil {
				t.Fatalf("failed to read container environment: %v", err)
			}
			allocated, err := draCPUSetFromEnvironment(environment)
			if err != nil {
				t.Fatalf("failed to find DRA CPU allocation: %v", err)
			}
			if allocated.Size() != 1 {
				t.Fatalf("container received %d DRA CPUs (%s), want 1", allocated.Size(), allocated.String())
			}

			effectiveOutput, err := execInPod(ctx, config, pod, "cat", "/sys/fs/cgroup/cpuset.cpus.effective")
			if err != nil {
				t.Fatalf("failed to read container effective CPU set: %v", err)
			}
			effective, err := cpuset.Parse(strings.TrimSpace(effectiveOutput))
			if err != nil {
				t.Fatalf("failed to parse container effective CPU set %q: %v", strings.TrimSpace(effectiveOutput), err)
			}
			if !effective.Equals(allocated) {
				t.Fatalf("container effective CPU set %s does not match DRA allocation %s", effective.String(), allocated.String())
			}
			return ctx
		}).
		Assess("container has example GPU allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			environment, err := execInPod(ctx, config, pod, "env")
			if err != nil {
				t.Fatalf("failed to read container environment: %v", err)
			}
			matches := gpuDeviceEnvironment.FindAllStringSubmatch(environment, -1)
			if len(matches) != 1 {
				t.Fatalf("container has %d example GPU allocations, want 1", len(matches))
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, "DRA resources allocated to container",
					slurmBridgeNamespace, "slurm", "kube-system", "dra-example-driver")
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for DRA pod cleanup: %v", err)
				return ctx
			}
			if err := crClient.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("failed to delete DRA pod %s: %v", podName, err)
			}
			return ctx
		}).
		Feature()
}
