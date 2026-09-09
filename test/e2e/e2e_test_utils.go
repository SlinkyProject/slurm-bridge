// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
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

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const (
	slurmNodeModeEnvironment    = "SLURM_NODE_MODE"
	e2eCleanupEnvironment       = "E2E_CLEANUP"
	slurmNodeModeExternal       = slurmNodeMode("external")
	slurmNodeModeHybrid         = slurmNodeMode("hybrid")
	slurmNamespace              = "slurm"
	slinkyNamespace             = "slinky"
	slurmControllerPodName      = "slurm-controller-0"
	slurmBridgeNamespace        = "slurm-bridge"
	slurmBridgePartition        = "slurm-bridge"
	slurmBridgeScheduler        = "slurm-bridge-scheduler"
	slurmNodeModeLabel          = "slurm-node-mode"
	slurmBridgeWorkerLabel      = "scheduler.slinky.slurm.net/slurm-bridge"
	slurmJobIDLabel             = "scheduler.slinky.slurm.net/slurm-jobid"
	slurmWorkerAppLabel         = "app.kubernetes.io/name"
	slurmWorkerAppValue         = "slurmd"
	slurmWorkerClusterLabel     = "slinky.slurm.net/cluster"
	slurmWorkerClusterValue     = "slurm"
	slurmWorkerScalingModeLabel = "nodeset.slinky.slurm.net/scaling-mode"
	slurmWorkerDaemonSetMode    = "DaemonSet"
	slurmJobStateCancelled      = "CANCELLED" //nolint:misspell // Slurm API spelling.
	draCPUResource              = "deviceclass.resource.kubernetes.io/dra.cpu"
	draExampleGPUResource       = "deviceclass.resource.kubernetes.io/gpu.example.com"
	slurmBridgeReadinessTimeout = 3 * time.Minute
	slurmWorkloadTimeout        = 10 * time.Minute
	slurmCleanupTimeout         = 3 * time.Minute
)

var (
	exampleGPUDeviceEnvironment = regexp.MustCompile(`(?m)^GPU_DEVICE_[0-9]+=(gpu-[0-9]+)$`)
)

type slurmNodeMode string

func parseSlurmNodeMode(value string) (slurmNodeMode, error) {
	mode := slurmNodeMode(value)
	switch mode {
	case slurmNodeModeExternal, slurmNodeModeHybrid:
		return mode, nil
	default:
		return "", fmt.Errorf("%s must be one of %q or %q, got %q",
			slurmNodeModeEnvironment, slurmNodeModeExternal, slurmNodeModeHybrid, value)
	}
}

func parseSlurmNodeModeFromEnvironment() (slurmNodeMode, error) {
	value := os.Getenv(slurmNodeModeEnvironment)
	if value == "" {
		value = string(slurmNodeModeExternal)
	}
	return parseSlurmNodeMode(value)
}

func parseE2ECleanupFromEnvironment() (bool, error) {
	value := os.Getenv(e2eCleanupEnvironment)
	if value == "" {
		return true, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean, got %q: %w", e2eCleanupEnvironment, value, err)
	}
	return enabled, nil
}

func e2eCleanupEnabled(t *testing.T) bool {
	t.Helper()
	enabled, err := parseE2ECleanupFromEnvironment()
	if err != nil {
		t.Errorf("invalid cleanup configuration: %v", err)
		return true
	}
	if !enabled {
		t.Logf("preserving test resources because %s=false", e2eCleanupEnvironment)
	}
	return enabled
}

func getControllerRuntimeClient(config *envconf.Config) (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := addReleaseSignalSchemes(scheme); err != nil {
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

func slurmNodeStates(output string) map[string]string {
	nodes := make(map[string]string)
	for line := range strings.Lines(output) {
		var name string
		var state string
		for field := range strings.FieldsSeq(line) {
			if value, found := strings.CutPrefix(field, "NodeName="); found {
				name = value
			}
			if value, found := strings.CutPrefix(field, "State="); found {
				state = value
			}
		}
		if name != "" {
			nodes[name] = state
		}
	}
	return nodes
}

func slurmJobNodeList(output string) (string, error) {
	return slurmJobField(output, "NodeList")
}

func slurmJobField(output, name string) (string, error) {
	for field := range strings.FieldsSeq(output) {
		if value, found := strings.CutPrefix(field, name+"="); found && value != "" && value != "(null)" {
			return value, nil
		}
	}
	return "", fmt.Errorf("slurm job output does not contain %s: %s", name, strings.TrimSpace(output))
}

func readyHybridWorkerNodes(pods []corev1.Pod) map[string]struct{} {
	nodes := make(map[string]struct{})
	for i := range pods {
		pod := &pods[i]
		if pod.Spec.NodeName == "" {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				nodes[pod.Spec.NodeName] = struct{}{}
				break
			}
		}
	}
	return nodes
}

func bridgeNodesReadyForMode(
	mode slurmNodeMode,
	bridgeNodes []corev1.Node,
	slurmStates map[string]string,
	readyHybridNodes map[string]struct{},
) (bool, string) {
	for i := range bridgeNodes {
		node := &bridgeNodes[i]
		state, registered := slurmStates[node.Name]
		if !registered {
			return false, fmt.Sprintf("Kubernetes bridge worker %s is not registered in Slurm", node.Name)
		}

		_, hasExternalLabel := node.Labels[wellknown.LabelExternalNode]
		switch mode {
		case slurmNodeModeExternal:
			if !hasExternalLabel {
				return false, fmt.Sprintf("external-mode worker %s does not have label %s",
					node.Name, wellknown.LabelExternalNode)
			}
			if !strings.Contains(state, "EXTERNAL") {
				return false, fmt.Sprintf("external-mode worker %s has Slurm state %q", node.Name, state)
			}
		case slurmNodeModeHybrid:
			if hasExternalLabel {
				return false, fmt.Sprintf("hybrid-mode worker %s unexpectedly has label %s",
					node.Name, wellknown.LabelExternalNode)
			}
			if strings.Contains(state, "EXTERNAL") {
				return false, fmt.Sprintf("hybrid-mode worker %s unexpectedly has Slurm state %q", node.Name, state)
			}
			if _, ready := readyHybridNodes[node.Name]; !ready {
				return false, fmt.Sprintf("hybrid-mode worker %s does not have a Ready DaemonSet slurmd pod", node.Name)
			}
		default:
			return false, fmt.Sprintf("unsupported Slurm node mode %q", mode)
		}
	}

	return true, fmt.Sprintf("all %d Kubernetes bridge workers are ready in %s mode", len(bridgeNodes), mode)
}

func testSlurmBridgeReadiness(nodeMode slurmNodeMode) types.Feature {
	return features.New("Slurm Bridge readiness").
		Assess(fmt.Sprintf("Slurm has all bridge workers in %s mode", nodeMode), func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}

			lastObservation := "readiness check did not run"
			if err := wait.For(func(ctx context.Context) (bool, error) {
				bridgeNodes := &corev1.NodeList{}
				if err := crClient.List(ctx, bridgeNodes,
					client.MatchingLabels{slurmBridgeWorkerLabel: "worker"},
				); err != nil {
					lastObservation = fmt.Sprintf("list Kubernetes bridge workers: %v", err)
					return false, nil
				}
				if len(bridgeNodes.Items) == 0 {
					lastObservation = "no Kubernetes bridge workers found"
					return false, nil
				}

				controllerPod := &corev1.Pod{}
				if err := crClient.Get(ctx, client.ObjectKey{
					Namespace: slurmNamespace,
					Name:      slurmControllerPodName,
				}, controllerPod); err != nil {
					lastObservation = fmt.Sprintf("get Slurm controller pod: %v", err)
					return false, nil
				}

				output, err := execInPod(ctx, config, controllerPod, "scontrol", "show", "nodes", "--oneliner")
				if err != nil {
					lastObservation = fmt.Sprintf("query Slurm nodes: %v", err)
					return false, nil
				}

				readyHybridNodes := map[string]struct{}{}
				if nodeMode == slurmNodeModeHybrid {
					slurmWorkerPods := &corev1.PodList{}
					if err := crClient.List(ctx, slurmWorkerPods,
						client.InNamespace(slurmNamespace),
						client.MatchingLabels{
							slurmWorkerAppLabel:         slurmWorkerAppValue,
							slurmWorkerClusterLabel:     slurmWorkerClusterValue,
							slurmWorkerScalingModeLabel: slurmWorkerDaemonSetMode,
						},
					); err != nil {
						lastObservation = fmt.Sprintf("list hybrid slurmd pods: %v", err)
						return false, nil
					}
					readyHybridNodes = readyHybridWorkerNodes(slurmWorkerPods.Items)
				}

				ready, observation := bridgeNodesReadyForMode(
					nodeMode,
					bridgeNodes.Items,
					slurmNodeStates(output),
					readyHybridNodes,
				)
				lastObservation = observation
				return ready, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmBridgeReadinessTimeout), wait.WithInterval(5*time.Second)); err != nil {
				t.Fatalf("Slurm Bridge never became ready in %s mode: %v; last observation: %s", nodeMode, err, lastObservation)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, "Slurm Bridge readiness", slurmNamespace, slinkyNamespace)
			}
			return ctx
		}).
		Feature()
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

	return features.New("Complete Kubernetes Job lifecycle").
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
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
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
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
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
		Assess("Job completes and releases its Slurm allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 1, func(pod *corev1.Pod) bool {
					return pod.Status.Phase == corev1.PodSucceeded &&
						!slices.Contains(pod.Finalizers, wellknown.FinalizerScheduler)
				})
			if err != nil {
				t.Fatalf("Job pod did not complete finalizer processing: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				observed := &batchv1.Job{}
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(job), observed); err != nil {
					return false, err
				}
				for _, condition := range observed.Status.Conditions {
					if condition.Type == batchv1.JobComplete {
						return condition.Status == corev1.ConditionTrue, nil
					}
				}
				return false, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(2*time.Second)); err != nil {
				t.Fatalf("Kubernetes Job did not report Complete: %v", err)
			}
			if err := waitForSlurmJobGone(ctx, config, crClient, pods[0].Labels[slurmJobIDLabel]); err != nil {
				t.Fatalf("Slurm allocation remained active after Job completion: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, "complete Kubernetes Job lifecycle",
					slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			}
			if !e2eCleanupEnabled(t) {
				return ctx
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
		Assess("pod runs on the worker node allocated by Slurm", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
					return false, err
				}
				return pod.Spec.NodeName != "" && pod.Labels[slurmJobIDLabel] != "", nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
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

			controllerPod := &corev1.Pod{}
			if err := crClient.Get(ctx, client.ObjectKey{
				Namespace: slurmNamespace,
				Name:      slurmControllerPodName,
			}, controllerPod); err != nil {
				t.Fatalf("failed to get Slurm controller pod: %v", err)
			}
			output, err := execInPod(ctx, config, controllerPod,
				"scontrol", "show", "job", pod.Labels[slurmJobIDLabel], "--oneliner")
			if err != nil {
				t.Fatalf("failed to query Slurm job %s: %v", pod.Labels[slurmJobIDLabel], err)
			}
			slurmNode, err := slurmJobNodeList(output)
			if err != nil {
				t.Fatal(err)
			}
			if slurmNode != pod.Spec.NodeName {
				t.Fatalf("Slurm allocated node %s, but Kubernetes bound the pod to %s", slurmNode, pod.Spec.NodeName)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, "Slurm-scheduled pod", slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			}
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for pod cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			return ctx
		}).
		Feature()
}

func testSlurmBridgeDRAResourceScheduling(exclusive bool) types.Feature {
	podName := envconf.RandomName("pod-dra-e2e", 32)
	exclusiveValue := "false"
	featureName := "Non-exclusive DRA resources allocated to container"
	if exclusive {
		exclusiveValue = "true"
		featureName = "Exclusive DRA resources allocated to container"
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			Namespace:   slurmBridgeNamespace,
			Annotations: map[string]string{wellknown.AnnotationExclusive: exclusiveValue},
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
							draExampleGPUResource: resource.MustParse("1"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("100Mi"),
							draCPUResource:        resource.MustParse("1"),
							draExampleGPUResource: resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	return features.New(featureName).
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
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
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
			if allocated.Size() == 0 {
				t.Fatal("container received an empty DRA CPU allocation")
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
			matches := exampleGPUDeviceEnvironment.FindAllStringSubmatch(environment, -1)
			if len(matches) != 1 {
				t.Fatalf("container has %d example GPU allocations, want 1", len(matches))
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, featureName,
					slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "kube-system", "dra-example-driver")
			}
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for DRA pod cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			return ctx
		}).
		Feature()
}

func testHybridSlurmBatchScheduling() types.Feature {
	var jobID string
	jobCompleted := false

	return features.New("Native Slurm batch scheduling").
		WithLabel(slurmNodeModeLabel, string(slurmNodeModeHybrid)).
		Assess("job submitted through Slurm runs on a hybrid worker", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}

			controllerPod := &corev1.Pod{}
			if err := crClient.Get(ctx, client.ObjectKey{
				Namespace: slurmNamespace,
				Name:      slurmControllerPodName,
			}, controllerPod); err != nil {
				t.Fatalf("failed to get Slurm controller pod: %v", err)
			}

			output, err := execInPod(ctx, config, controllerPod,
				"sbatch", "--parsable", "--partition="+slurmBridgePartition,
				"--mem=100M", "--chdir=/tmp", "--output=/dev/null", "--wrap=/bin/true")
			if err != nil {
				t.Fatalf("failed to submit native Slurm job: %v", err)
			}
			jobID, _, _ = strings.Cut(strings.TrimSpace(output), ";")
			if jobID == "" {
				t.Fatalf("sbatch did not return a job ID: %q", output)
			}

			var jobOutput string
			var lastObservation string
			if err := wait.For(func(ctx context.Context) (bool, error) {
				jobOutput, err = execInPod(ctx, config, controllerPod,
					"scontrol", "show", "job", jobID, "--oneliner")
				if err != nil {
					return false, fmt.Errorf("query native Slurm job %s: %w", jobID, err)
				}

				state, err := slurmJobField(jobOutput, "JobState")
				if err != nil {
					return false, err
				}
				lastObservation = "JobState=" + state
				switch state {
				case "COMPLETED":
					return true, nil
				case "BOOT_FAIL", slurmJobStateCancelled, "DEADLINE", "FAILED", "NODE_FAIL",
					"OUT_OF_MEMORY", "PREEMPTED", "REVOKED", "TIMEOUT":
					return false, fmt.Errorf("native Slurm job %s reached terminal state %s", jobID, state)
				default:
					return false, nil
				}
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(time.Second)); err != nil {
				t.Fatalf("native Slurm job %s did not complete: %v; last observation: %s", jobID, err, lastObservation)
			}
			jobCompleted = true

			nodeName, err := slurmJobNodeList(jobOutput)
			if err != nil {
				t.Fatal(err)
			}
			node := &corev1.Node{}
			if err := crClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
				t.Fatalf("failed to get Kubernetes node %s allocated by Slurm: %v", nodeName, err)
			}
			if node.Labels[slurmBridgeWorkerLabel] != "worker" {
				t.Fatalf("native Slurm job ran on node %s which is not a Slurm Bridge worker", nodeName)
			}
			if _, external := node.Labels[wellknown.LabelExternalNode]; external {
				t.Fatalf("native Slurm job ran on external node %s", nodeName)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, "native Slurm batch job", slurmNamespace, slinkyNamespace)
			}
			if jobID == "" || jobCompleted || !e2eCleanupEnabled(t) {
				return ctx
			}

			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Logf("failed to get client while canceling native Slurm job %s: %v", jobID, err)
				return ctx
			}
			controllerPod := &corev1.Pod{}
			if err := crClient.Get(ctx, client.ObjectKey{
				Namespace: slurmNamespace,
				Name:      slurmControllerPodName,
			}, controllerPod); err != nil {
				t.Logf("failed to get Slurm controller pod while canceling native job %s: %v", jobID, err)
				return ctx
			}
			if _, err := execInPod(ctx, config, controllerPod, "scancel", jobID); err != nil {
				t.Logf("failed to cancel native Slurm job %s: %v", jobID, err)
			}
			return ctx
		}).
		Feature()
}
