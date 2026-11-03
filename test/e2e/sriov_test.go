// SPDX-FileCopyrightText: Copyright (C) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
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

// Fake sysfs and PTP exercise DRA/CNI integration, not physical VF attachment.
func testSlurmBridgeSRIOVResourceScheduling() types.Feature {
	const driver = "sriovnetwork.k8snetworkplumbingwg.io"
	var nodeName, deviceName string
	var pods []*corev1.Pod
	feature := features.New("SR-IOV allocation and reuse")
	for cycle := range 2 {
		feature.Assess(fmt.Sprintf("allocation %d configures the interface", cycle+1), func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			requests := corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
				corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "sriov-vf"): resource.MustParse("1"),
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: envconf.RandomName("sriov-e2e", 32), Namespace: slurmBridgeNamespace},
				Spec: corev1.PodSpec{
					SchedulerName: slurmBridgeScheduler,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name: "network", Image: "busybox:stable", Command: []string{"sleep", "600"},
						SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"}}},
						Resources:       corev1.ResourceRequirements{Requests: requests, Limits: requests.DeepCopy()},
					}},
				},
			}
			if nodeName != "" {
				// The fixture advertises one VF per node. Pin reuse through the
				// scheduler, rather than bypassing scheduling with spec.nodeName.
				pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": nodeName}
			}
			if err := c.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			pods = append(pods, pod)
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
					return false, err
				}
				if pod.Status.Phase == corev1.PodFailed {
					return false, fmt.Errorf("SR-IOV pod failed: %s", statusJSON(pod.Status))
				}
				return pod.Status.Phase == corev1.PodRunning, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(2*time.Second)); err != nil {
				t.Fatalf("SR-IOV pod did not run: %v; status: %s", err, statusJSON(pod.Status))
			}
			claims := &resourcev1.ResourceClaimList{}
			if err := c.List(ctx, claims, client.InNamespace(slurmBridgeNamespace)); err != nil {
				t.Fatal(err)
			}
			var claim *resourcev1.ResourceClaim
			for i := range claims.Items {
				if metav1.IsControlledBy(&claims.Items[i], pod) {
					claim = &claims.Items[i]
					break
				}
			}
			if claim == nil || claim.Status.Allocation == nil || len(claim.Status.Allocation.Devices.Results) != 1 {
				t.Fatalf("expected one allocated VF, got claim: %s", statusJSON(claim))
			}
			result := claim.Status.Allocation.Devices.Results[0]
			if result.Driver != driver || result.Pool != pod.Spec.NodeName || result.Device != "0000-01-00-1" {
				t.Fatalf("unexpected fixture allocation: %+v", result)
			}
			if cycle > 0 && (nodeName != pod.Spec.NodeName || deviceName != result.Device) {
				t.Fatalf("expected reuse of %s/%s, got %s/%s", nodeName, deviceName, pod.Spec.NodeName, result.Device)
			}
			nodeName, deviceName = pod.Spec.NodeName, result.Device
			configured := false
			for _, allocationConfig := range claim.Status.Allocation.Devices.Config {
				opaque := allocationConfig.Opaque
				if allocationConfig.Source != resourcev1.AllocationConfigSourceClass || opaque == nil || opaque.Driver != driver {
					continue
				}
				var parameters map[string]any
				if err := json.Unmarshal(opaque.Parameters.Raw, &parameters); err != nil {
					t.Fatal(err)
				}
				configured = parameters["kind"] == "VfConfig" && parameters["netAttachDefName"] == "sriov-vf" && parameters["ifName"] == "sriovkind0" && len(allocationConfig.Requests) == 1 && allocationConfig.Requests[0] == result.Request
			}
			if !configured {
				t.Fatalf("claim lacks request-scoped class VfConfig: %s", statusJSON(claim.Status.Allocation))
			}
			controller := &corev1.Pod{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: slurmNamespace, Name: slurmControllerPodName}, controller); err != nil {
				t.Fatal(err)
			}
			output, err := execInPod(ctx, config, controller, "scontrol", "show", "node", nodeName, "--oneliner")
			if err != nil || !strings.Contains(output, "nic:sriov-vf:1") {
				t.Fatalf("missing SR-IOV Slurm GRES: %v; %s", err, output)
			}
			output, err = execInPod(ctx, config, pod, "ip", "address", "show", "sriovkind0")
			if err != nil || !strings.Contains(output, "UP") || !strings.Contains(output, "inet 10.245.") {
				t.Fatalf("missing configured PTP interface: %v; %s", err, output)
			}
			t.Logf("Allocated %s/%s; class configuration applied; interface: %s", nodeName, deviceName, output)
			deletePodsAndAssertCleanup(ctx, t, config, c, pod)
			return ctx
		})
	}
	feature.Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
		captureReleaseSignalDiagnostics(t, "SR-IOV allocation and reuse", slurmBridgeNamespace, slurmNamespace, "dra-driver-sriov")
		if e2eCleanupEnabled(t) {
			c, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Error(err)
				return ctx
			}
			deletePodsAndAssertCleanup(ctx, t, config, c, pods...)
		}
		return ctx
	})
	return feature.Feature()
}
