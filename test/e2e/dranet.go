// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

const (
	dranetDeviceName = "dranet0"
	dranetDriver     = "dra.net"
	dranetSelector   = `device.driver == 'dra.net' && has(device.attributes['dra.net'].ifName) && device.attributes['dra.net'].ifName == 'dranet0'`
)

func testSlurmBridgeDRANETResourceScheduling() types.Feature {
	deviceClassName := envconf.RandomName("dranet-e2e", 32)
	jobName := envconf.RandomName("job-dranet-e2e", 32)
	deviceResource := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + deviceClassName)
	deviceClass := &resourcev1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: deviceClassName},
		Spec: resourcev1.DeviceClassSpec{
			Selectors: []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{Expression: dranetSelector},
			}},
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: slurmBridgeNamespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					SchedulerName: slurmBridgeScheduler,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "network",
						Image:   "busybox:stable",
						Command: []string{"sh", "-c", "ip link show dranet0; sleep 300"},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("16Mi"),
								deviceResource:        resource.MustParse("1"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("16Mi"),
								deviceResource:        resource.MustParse("1"),
							},
						},
					}},
				},
			},
		},
	}
	pod := &corev1.Pod{}
	claim := &resourcev1.ResourceClaim{}

	return features.New("DRANET device allocated to container").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := crClient.Create(ctx, deviceClass); err != nil {
				t.Fatalf("failed to create DRANET DeviceClass: %v", err)
			}
			if err := crClient.Create(ctx, job); err != nil {
				t.Fatalf("failed to create DRANET job: %v", err)
			}
			return ctx
		}).
		Assess("job pod reaches Running", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				pods := &corev1.PodList{}
				if err := crClient.List(ctx, pods,
					client.InNamespace(slurmBridgeNamespace),
					client.MatchingLabels{batchv1.JobNameLabel: jobName},
				); err != nil {
					return false, err
				}
				if len(pods.Items) == 0 {
					return false, nil
				}
				*pod = pods.Items[0]
				if pod.Status.Phase == corev1.PodFailed {
					return false, fmt.Errorf("DRANET pod failed: %s", pod.Status.Message)
				}
				return pod.Status.Phase == corev1.PodRunning, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
				t.Fatalf("DRANET pod never reached Running: %v; observed status: %s", err, statusJSON(pod.Status))
			}
			return ctx
		}).
		Assess("ResourceClaim allocates dranet0 on the pod node", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				claims := &resourcev1.ResourceClaimList{}
				if err := crClient.List(ctx, claims, client.InNamespace(slurmBridgeNamespace)); err != nil {
					return false, err
				}
				for i := range claims.Items {
					candidate := &claims.Items[i]
					if !metav1.IsControlledBy(candidate, pod) || candidate.Status.Allocation == nil {
						continue
					}
					for _, result := range candidate.Status.Allocation.Devices.Results {
						if result.Driver == dranetDriver && result.Pool == pod.Spec.NodeName && result.Device == dranetDeviceName {
							*claim = *candidate
							return true, nil
						}
					}
				}
				return false, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
				t.Fatalf("ResourceClaim never allocated %s/%s/%s: %v", dranetDriver, pod.Spec.NodeName, dranetDeviceName, err)
			}
			return ctx
		}).
		Assess("DRANET allocation reports network ready", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
					return false, err
				}
				for _, status := range claim.Status.Devices {
					if status.Driver != dranetDriver || status.Pool != pod.Spec.NodeName || status.Device != dranetDeviceName {
						continue
					}
					return status.NetworkData != nil && status.NetworkData.InterfaceName == dranetDeviceName &&
						apimeta.IsStatusConditionTrue(status.Conditions, "Ready") &&
						apimeta.IsStatusConditionTrue(status.Conditions, "NetworkReady"), nil
				}
				return false, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
				t.Fatalf("DRANET allocation never reported network ready: %v; observed status: %s", err, statusJSON(claim.Status.Devices))
			}
			return ctx
		}).
		Assess("Slurm advertises the DRANET device as indexed GRES", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			controllerPod := &corev1.Pod{}
			if err := crClient.Get(ctx, client.ObjectKey{Namespace: slurmNamespace, Name: slurmControllerPodName}, controllerPod); err != nil {
				t.Fatalf("failed to get Slurm controller pod: %v", err)
			}
			output, err := execInPod(ctx, config, controllerPod, "scontrol", "show", "node", pod.Spec.NodeName, "--oneliner")
			if err != nil {
				t.Fatalf("failed to inspect Slurm node %s: %v", pod.Spec.NodeName, err)
			}
			if !strings.Contains(output, "nic:dranet0:1") {
				t.Fatalf("Slurm node %s does not advertise nic:dranet0:1: %s", pod.Spec.NodeName, output)
			}
			return ctx
		}).
		Assess("container has the allocated DRANET interface", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			output, err := execInPod(ctx, config, pod, "ip", "link", "show", dranetDeviceName)
			if err != nil {
				t.Fatalf("failed to inspect DRANET interface: %v", err)
			}
			if !strings.Contains(output, dranetDeviceName+":") || !strings.Contains(output, "UP") {
				t.Fatalf("DRANET interface is not up inside the container: %s", output)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "DRANET device allocated to container",
				slurmBridgeNamespace, slurmNamespace, "kube-system")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for DRANET cleanup: %v", err)
				return ctx
			}
			podList := &corev1.PodList{}
			if err := crClient.List(ctx, podList,
				client.InNamespace(slurmBridgeNamespace),
				client.MatchingLabels{batchv1.JobNameLabel: jobName},
			); err != nil {
				t.Errorf("failed to list DRANET job pods for cleanup: %v", err)
			}
			pods := make([]*corev1.Pod, len(podList.Items))
			for i := range podList.Items {
				pods[i] = &podList.Items[i]
			}
			deleteObject(t, ctx, crClient, job)
			deletePodsAndAssertCleanup(ctx, t, config, crClient, pods...)
			deleteObject(t, ctx, crClient, deviceClass)
			return ctx
		}).
		Feature()
}
