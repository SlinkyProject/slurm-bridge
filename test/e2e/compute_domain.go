// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

const (
	computeDomainDriver        = "compute-domain.nvidia.com"
	computeDomainChannelClass  = "compute-domain-default-channel.nvidia.com"
	computeDomainChannelDevice = "channel-0"
	computeDomainNodeLabel     = "resource.nvidia.com/computeDomain"
	computeDomainPods          = 2
)

var computeDomainGVK = schema.GroupVersionKind{Group: "resource.nvidia.com", Version: "v1beta1", Kind: "ComputeDomain"}

// testSlurmBridgeComputeDomainScheduling runs a two-node ComputeDomain workload.
// A per-ComputeDomain DeviceClass carries the domain ID, because bridge Pods
// cannot reference the driver's ResourceClaimTemplate. Mocked IMEX exercises
// scheduling and channel preparation, not memory exchange between GPUs.
func testSlurmBridgeComputeDomainScheduling(required bool) types.Feature {
	const featureName = "NVIDIA ComputeDomain IMEX channel allocated to containers"
	name := envconf.RandomName("cd-e2e", 24)
	computeDomain := &unstructured.Unstructured{}
	computeDomain.SetGroupVersionKind(computeDomainGVK)
	computeDomain.SetNamespace(slurmBridgeNamespace)
	computeDomain.SetName(name)
	computeDomain.Object["spec"] = map[string]any{
		"numNodes": int64(0),
		"channel": map[string]any{
			"allocationMode":        "Single",
			"resourceClaimTemplate": map[string]any{"name": name + "-channel"},
		},
	}
	deviceClass := &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: name}}
	var pods []*corev1.Pod
	var domainID string

	return features.New(featureName).
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			// The bridge resolves a DeviceClass by its exact selector, so reuse
			// the selector of the driver's default channel class.
			channelClass := &resourcev1.DeviceClass{}
			if err := c.Get(ctx, client.ObjectKey{Name: computeDomainChannelClass}, channelClass); err != nil {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("get DeviceClass %s: %v", computeDomainChannelClass, err)
				}
				if required {
					t.Fatalf("MOCK_NVML is enabled, but DeviceClass %s does not exist", computeDomainChannelClass)
				}
				t.Skipf("DeviceClass %s does not exist; NVIDIA ComputeDomains are not enabled", computeDomainChannelClass)
			}
			if err := c.Create(ctx, computeDomain); err != nil {
				t.Fatalf("create ComputeDomain: %v", err)
			}
			domainID = string(computeDomain.GetUID())
			parameters, err := json.Marshal(map[string]string{
				"apiVersion":     computeDomainGVK.GroupVersion().String(),
				"kind":           "ComputeDomainChannelConfig",
				"domainID":       domainID,
				"allocationMode": "Single",
			})
			if err != nil {
				t.Fatal(err)
			}
			deviceClass.Spec = resourcev1.DeviceClassSpec{
				Selectors: channelClass.Spec.Selectors,
				Config: []resourcev1.DeviceClassConfiguration{{
					DeviceConfiguration: resourcev1.DeviceConfiguration{
						Opaque: &resourcev1.OpaqueDeviceConfiguration{
							Driver:     computeDomainDriver,
							Parameters: runtime.RawExtension{Raw: parameters},
						},
					},
				}},
			}
			if err := c.Create(ctx, deviceClass); err != nil {
				t.Fatalf("create DeviceClass %s: %v", deviceClass.Name, err)
			}

			channelResource := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + deviceClass.Name)
			for i := range computeDomainPods {
				requests := corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
					draNvidiaGPUResource:  resource.MustParse("1"),
					channelResource:       resource.MustParse("1"),
				}
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", name, i), Namespace: slurmBridgeNamespace},
					Spec: corev1.PodSpec{
						SchedulerName: slurmBridgeScheduler,
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{{
							Name:    "worker",
							Image:   "busybox:stable",
							Command: []string{"sh", "-c", "test -e /dev/nvidia-caps-imex-channels/channel0 && sleep 600"},
							Resources: corev1.ResourceRequirements{
								Requests: requests,
								Limits:   requests.DeepCopy(),
							},
						}},
					},
				}
				if err := c.Create(ctx, pod); err != nil {
					t.Fatalf("create pod %s: %v", pod.Name, err)
				}
				pods = append(pods, pod)
			}
			return ctx
		}).
		Assess("pods run on distinct nodes with the IMEX channel", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			nodes := map[string]struct{}{}
			for _, pod := range pods {
				if err := wait.For(func(ctx context.Context) (bool, error) {
					if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
						return false, err
					}
					if pod.Status.Phase == corev1.PodFailed {
						return false, fmt.Errorf("pod failed: %s", pod.Status.Message)
					}
					return pod.Status.Phase == corev1.PodRunning, nil
				}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
					t.Fatalf("pod %s never reached Running: %v; observed status: %s", pod.Name, err, statusJSON(pod.Status))
				}
				nodes[pod.Spec.NodeName] = struct{}{}
				output, err := execInPod(ctx, config, pod, "ls", "/dev/nvidia-caps-imex-channels")
				if err != nil {
					t.Fatalf("list IMEX channels in pod %s: %v", pod.Name, err)
				}
				if channels := strings.Fields(output); !slices.Equal(channels, []string{"channel0"}) {
					t.Fatalf("pod %s has IMEX channels %q, want [channel0]", pod.Name, channels)
				}
			}
			// Each node publishes one channel device, so Slurm must spread the pods.
			if len(nodes) != computeDomainPods {
				t.Fatalf("%d pods ran on %d nodes, want one pod per node", computeDomainPods, len(nodes))
			}
			return ctx
		}).
		Assess("claims carry the channel and the ComputeDomain ID", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			for _, pod := range pods {
				status := pod.Status.ExtendedResourceClaimStatus
				if status == nil || status.ResourceClaimName == "" {
					t.Fatalf("pod %s has no generated ResourceClaim", pod.Name)
				}
				claim := &resourcev1.ResourceClaim{}
				if err := c.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: status.ResourceClaimName}, claim); err != nil {
					t.Fatalf("get ResourceClaim %s: %v", status.ResourceClaimName, err)
				}
				if err := verifyComputeDomainClaim(claim, domainID); err != nil {
					t.Fatalf("pod %s: %v", pod.Name, err)
				}
			}
			return ctx
		}).
		Assess("Slurm jobs hold the IMEX channel GRES", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			for _, pod := range pods {
				jobID := pod.Labels[slurmJobIDLabel]
				if jobID == "" {
					t.Fatalf("pod %s has no Slurm job ID label", pod.Name)
				}
				output, err := querySlurmJob(ctx, config, c, jobID)
				if err != nil {
					t.Fatalf("query Slurm job %s: %v", jobID, err)
				}
				tres, err := slurmJobField(output, "TresPerNode")
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(tres, "gres/imex:imex-channel=1") {
					t.Fatalf("Slurm job %s TresPerNode %q does not request gres/imex:imex-channel=1", jobID, tres)
				}
			}
			return ctx
		}).
		Assess("ComputeDomain is Ready on the pod nodes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			want := make([]string, 0, len(pods))
			for _, pod := range pods {
				want = append(want, pod.Spec.NodeName)
			}
			slices.Sort(want)
			observed := &unstructured.Unstructured{}
			observed.SetGroupVersionKind(computeDomainGVK)
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := c.Get(ctx, client.ObjectKeyFromObject(computeDomain), observed); err != nil {
					return false, err
				}
				status, _, _ := unstructured.NestedString(observed.Object, "status", "status")
				nodes, _, _ := unstructured.NestedSlice(observed.Object, "status", "nodes")
				var ready []string
				for _, node := range nodes {
					fields, _ := node.(map[string]any)
					if fields["status"] == "Ready" {
						name, _ := fields["name"].(string)
						ready = append(ready, name)
					}
				}
				slices.Sort(ready)
				return status == "Ready" && slices.Equal(ready, want), nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
				t.Fatalf("ComputeDomain never reported Ready on %v: %v; observed status: %s", want, err, statusJSON(observed.Object["status"]))
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, featureName,
					slurmBridgeNamespace, "slurm", "dra-driver-nvidia-gpu", "nvml-mock")
			}
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for ComputeDomain cleanup: %v", err)
				return ctx
			}
			nodeNames := make([]string, 0, len(pods))
			for _, pod := range pods {
				if pod.Spec.NodeName != "" {
					nodeNames = append(nodeNames, pod.Spec.NodeName)
				}
			}
			deletePodsAndAssertCleanup(ctx, t, config, c, pods...)
			// Unpreparing the last channel claim on a node removes its label,
			// which also stops the node's IMEX daemon.
			for _, nodeName := range nodeNames {
				if err := wait.For(func(ctx context.Context) (bool, error) {
					node := &corev1.Node{}
					if err := c.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
						return false, err
					}
					return node.Labels[computeDomainNodeLabel] != domainID, nil
				}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
					t.Errorf("node %s kept label %s=%s after its pod was deleted: %v", nodeName, computeDomainNodeLabel, domainID, err)
				}
			}
			deleteObject(t, ctx, c, deviceClass)
			deleteObject(t, ctx, c, computeDomain)
			return ctx
		}).
		Feature()
}

// verifyComputeDomainClaim checks that a generated claim allocated the node's
// IMEX channel with the DeviceClass configuration naming the ComputeDomain.
func verifyComputeDomainClaim(claim *resourcev1.ResourceClaim, domainID string) error {
	if claim.Status.Allocation == nil {
		return fmt.Errorf("ResourceClaim %s is not allocated", claim.Name)
	}
	var channelRequest string
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver == computeDomainDriver {
			if result.Device != computeDomainChannelDevice || channelRequest != "" {
				return fmt.Errorf("ResourceClaim %s allocated %s device %s, want exactly one %s", claim.Name, computeDomainDriver, result.Device, computeDomainChannelDevice)
			}
			channelRequest = result.Request
		}
	}
	if channelRequest == "" {
		return fmt.Errorf("ResourceClaim %s has no %s device", claim.Name, computeDomainDriver)
	}
	for _, config := range claim.Status.Allocation.Devices.Config {
		if config.Source != resourcev1.AllocationConfigSourceClass || config.Opaque == nil ||
			config.Opaque.Driver != computeDomainDriver || !slices.Contains(config.Requests, channelRequest) {
			continue
		}
		var parameters struct {
			Kind     string `json:"kind"`
			DomainID string `json:"domainID"`
		}
		if err := json.Unmarshal(config.Opaque.Parameters.Raw, &parameters); err != nil {
			return fmt.Errorf("decode ResourceClaim %s channel configuration: %w", claim.Name, err)
		}
		if parameters.Kind == "ComputeDomainChannelConfig" && parameters.DomainID == domainID {
			return nil
		}
	}
	return fmt.Errorf("ResourceClaim %s has no %s ComputeDomainChannelConfig for domain %s on request %s", claim.Name, resourcev1.AllocationConfigSourceClass, domainID, channelRequest)
}
