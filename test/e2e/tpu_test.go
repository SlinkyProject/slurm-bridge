// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

// TestTPUAllocation checks whole-host allocation, cleanup, and reuse in Kind.
func TestTPUAllocation(t *testing.T) {
	requireE2ESuite(t, "tpu", true)
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseE2ECleanupFromEnvironment(); err != nil {
		t.Fatal(err)
	}
	var nodeName string
	for attempt := 1; attempt <= 2; attempt++ {
		feature := testTPUAllocation(fmt.Sprintf("TPU whole-host reuse %d", attempt), &nodeName)
		testEnv.Test(t, withSuiteLabel(feature, "tpu"))
		if t.Failed() || !e2eCleanupEnabled(t) {
			break
		}
	}
}

// An empty nodeName lets Slurm choose a host; subsequent calls reuse that host.
func testTPUAllocation(name string, nodeName *string) types.Feature {
	const resourceName corev1.ResourceName = "deviceclass.resource.kubernetes.io/tpu.google.com"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envconf.RandomName("tpu-e2e", 32),
			Namespace: slurmBridgeNamespace,
		},
		Spec: corev1.PodSpec{
			SchedulerName: slurmBridgeScheduler,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "tpu",
				Image:   "busybox:stable",
				Command: []string{"sh", "-c", "sleep 600"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
						resourceName:          resource.MustParse("4"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
						resourceName:          resource.MustParse("4"),
					},
				},
			}},
		},
	}
	return features.New(name).
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			if *nodeName != "" {
				node := &corev1.Node{}
				if err := c.Get(ctx, client.ObjectKey{Name: *nodeName}, node); err != nil {
					t.Fatal(err)
				}
				hostname := node.Labels[corev1.LabelHostname]
				if hostname == "" {
					t.Fatal("fixture worker has no hostname label")
				}
				pod.Spec.NodeSelector = map[string]string{corev1.LabelHostname: hostname}
			}
			if err := c.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Assess("Slurm allocation reaches Running with four TPU devices", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
					return false, err
				}
				if pod.Status.Phase == corev1.PodFailed {
					return false, fmt.Errorf("pod failed: %s", statusJSON(pod.Status))
				}
				return pod.Status.Phase == corev1.PodRunning, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(2*time.Second)); err != nil {
				t.Fatalf("TPU pod did not run: %v; %s", err, statusJSON(pod.Status))
			}
			if *nodeName != "" && pod.Spec.NodeName != *nodeName {
				t.Fatalf("pod landed on %s, want %s", pod.Spec.NodeName, *nodeName)
			}
			*nodeName = pod.Spec.NodeName
			if pod.Labels[slurmJobIDLabel] == "" {
				t.Fatal("missing Slurm job ID")
			}
			status := pod.Status.ExtendedResourceClaimStatus
			if status == nil {
				t.Fatal("missing generated claim reference")
			}
			if len(status.RequestMappings) != 1 || status.RequestMappings[0].ResourceName != string(resourceName) {
				t.Fatalf("claim mappings = %+v, want original resource %q", status.RequestMappings, resourceName)
			}
			claim := &resourcev1.ResourceClaim{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: status.ResourceClaimName}, claim); err != nil {
				t.Fatal(err)
			}
			if !metav1.IsControlledBy(claim, pod) || claim.Status.Allocation == nil {
				t.Fatal("missing owned, allocated claim")
			}
			var devices []string
			for _, result := range claim.Status.Allocation.Devices.Results {
				if result.Driver != "tpu.google.com" || result.Pool != *nodeName {
					t.Fatalf("unexpected allocation: %+v", result)
				}
				devices = append(devices, result.Device)
			}
			slices.Sort(devices)
			if !slices.Equal(devices, []string{"accel0", "accel1", "accel2", "accel3"}) {
				t.Fatalf("allocated devices: %v", devices)
			}
			controller := &corev1.Pod{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: slurmNamespace, Name: slurmControllerPodName}, controller); err != nil {
				t.Fatal(err)
			}
			output, err := execInPod(ctx, config, controller, "scontrol", "show", "node", *nodeName, "--oneliner")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, "tpu:tpu.google.com:4") {
				t.Fatalf("missing TPU GRES: %s", output)
			}
			output, err = execInPod(ctx, config, controller, "scontrol", "show", "job", pod.Labels[slurmJobIDLabel], "--details")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, "tpu:tpu.google.com:4") {
				t.Fatalf("job has no four-chip TPU allocation: %s", output)
			}
			output, err = execInPod(ctx, config, pod, "sh", "-ec", `for i in 0 1 2 3; do test -b /dev/accel$i; done
test "$TPU_TOPOLOGY" = 2x2
test "$TPU_CHIPS_PER_HOST_BOUNDS" = 2,2,1
test "$TPU_WORKER_ID" = 0`)
			if err != nil {
				t.Fatalf("TPU CDI device/environment check: %v; %s", err, output)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "TPU allocation", slurmBridgeNamespace, slurmNamespace, "dra-driver-google-tpu")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Error(err)
				return ctx
			}
			deletePodsAndAssertCleanup(ctx, t, config, c, pod)
			return ctx
		}).Feature()
}

func getTPUAliasClass(ctx context.Context, t *testing.T, c client.Client) *resourcev1.DeviceClass {
	t.Helper()
	class := &resourcev1.DeviceClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: "tpu-ray"}, class); err != nil {
		t.Fatal(err)
	}
	if class.Spec.ExtendedResourceName == nil || *class.Spec.ExtendedResourceName != "google.com/tpu" {
		t.Fatal("TPU fixture must expose google.com/tpu through DeviceClass tpu-ray")
	}
	return class
}

func assertTPUAliasClaim(ctx context.Context, t *testing.T, c client.Client, worker *corev1.Pod, className string) {
	t.Helper()
	status := worker.Status.ExtendedResourceClaimStatus
	if status == nil || len(status.RequestMappings) != 1 || status.RequestMappings[0].ResourceName != "google.com/tpu" {
		t.Fatalf("worker claim status = %+v", status)
	}
	claim := &resourcev1.ResourceClaim{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: worker.Namespace, Name: status.ResourceClaimName}, claim); err != nil {
		t.Fatal(err)
	}
	if len(claim.Spec.Devices.Requests) != 1 || claim.Spec.Devices.Requests[0].Exactly == nil || claim.Spec.Devices.Requests[0].Exactly.DeviceClassName != className || claim.Status.Allocation == nil || len(claim.Status.Allocation.Devices.Results) != 4 {
		t.Fatalf("unexpected worker claim: %s", statusJSON(claim))
	}
}
