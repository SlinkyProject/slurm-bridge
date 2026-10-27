// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const draNvidiaGPUDriver = "gpu.nvidia.com"

type numaTopology struct {
	devices map[structured.DeviceID][]int64
	cpus    map[int64]cpuset.CPUSet
}

// nodeNUMATopology reads device NUMA nodes from one node's ResourceSlices. CPU
// devices use their own NUMA node rather than the nodes local to them.
func nodeNUMATopology(ctx context.Context, c client.Client, nodeName string) (numaTopology, error) {
	topology := numaTopology{devices: make(map[structured.DeviceID][]int64), cpus: make(map[int64]cpuset.CPUSet)}
	resourceSlices := &resourcev1.ResourceSliceList{}
	if err := c.List(ctx, resourceSlices); err != nil {
		return topology, err
	}
	for _, resourceSlice := range resourceSlices.Items {
		if ptr.Deref(resourceSlice.Spec.NodeName, "") != nodeName {
			continue
		}
		for _, device := range resourceSlice.Spec.Devices {
			id := structured.MakeDeviceID(resourceSlice.Spec.Driver, resourceSlice.Spec.Pool.Name, device.Name)
			if numa, ok := device.Attributes[nodeinfo.DraDriverCpu_NUMANodeID]; ok && numa.IntValue != nil {
				cpus, err := cpuset.Parse(ptr.Deref(device.Attributes[nodeinfo.DraDriverCpu_CPUIDs].StringValue, ""))
				if err != nil {
					return topology, fmt.Errorf("CPU device %s: %w", id, err)
				}
				topology.devices[id] = []int64{*numa.IntValue}
				topology.cpus[*numa.IntValue] = cpus
			} else if numa, ok := device.Attributes[deviceattribute.StandardDeviceAttributeNUMANode]; ok {
				topology.devices[id] = numa.IntValues
				if numa.IntValue != nil {
					topology.devices[id] = []int64{*numa.IntValue}
				}
			}
		}
	}
	return topology, nil
}

// requireNUMAAlignment returns a worker with NUMA-grouped CPUs and NUMA-local
// GPUs and NICs. MOCK_NVML clusters must have one.
func requireNUMAAlignment(ctx context.Context, t *testing.T, config *envconf.Config, c client.Client, required bool) string {
	t.Helper()
	requireKubernetes(t, config, "1.37", "NUMA alignment")
	nodes := &corev1.NodeList{}
	if err := c.List(ctx, nodes, client.MatchingLabels{slurmBridgeWorkerLabel: "worker", nvidiaGPUPresentLabel: "true"}); err != nil {
		t.Fatalf("list NVIDIA GPU workers: %v", err)
	}
	for _, node := range nodes.Items {
		topology, err := nodeNUMATopology(ctx, c, node.Name)
		if err != nil {
			t.Fatal(err)
		}
		drivers := sets.New[string]()
		for id := range topology.devices {
			drivers.Insert(id.Driver.String())
		}
		if len(topology.cpus) > 1 && drivers.HasAll(draNvidiaGPUDriver, dranetDriver) {
			return node.Name
		}
	}
	if required {
		t.Fatal("MOCK_NVML is enabled, but no Slurm Bridge worker publishes NUMA-local CPUs, GPUs, and NICs")
	}
	t.Skip("no Slurm Bridge worker publishes NUMA-local CPUs, GPUs, and NICs")
	return ""
}

// testNUMAAlignment checks that a shared job's CPUs, GPU, and NIC are aligned.
func testNUMAAlignment(required bool) types.Feature {
	featureName := "GPU, NIC and CPUs allocated from one NUMA node"
	podName := envconf.RandomName("pod-numa-e2e", 32)
	deviceClassName := envconf.RandomName("dranet-numa-e2e", 32)
	nicResource := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + deviceClassName)
	deviceClass := &resourcev1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: deviceClassName},
		Spec: resourcev1.DeviceClassSpec{
			Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: dranetSelector}}},
		},
	}
	resources := corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("100Mi"),
		draCPUResource:        resource.MustParse("2"),
		draNvidiaGPUResource:  resource.MustParse("1"),
		nicResource:           resource.MustParse("1"),
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: slurmBridgeNamespace,
			// A shared allocation leaves Slurm to choose the cores.
			Annotations: map[string]string{wellknown.AnnotationExclusive: "false"},
		},
		Spec: corev1.PodSpec{
			SchedulerName: slurmBridgeScheduler,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:      "numa",
				Image:     "busybox:stable",
				Command:   []string{"sh", "-c", "sleep 300"},
				Resources: corev1.ResourceRequirements{Requests: resources, Limits: resources},
			}},
		},
	}
	return features.New(featureName).
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireCPUDRA(t, config)
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			nodeName := requireNUMAAlignment(ctx, t, config, crClient, required)
			pod.Spec.NodeSelector = map[string]string{corev1.LabelHostname: nodeName}
			if err := crClient.Create(ctx, deviceClass); err != nil {
				t.Fatalf("failed to create DRANET DeviceClass: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("failed to create NUMA alignment pod: %v", err)
			}
			return ctx
		}).
		Assess("pod reaches Running", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("failed to get client: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
					return false, err
				}
				if pod.Status.Phase == corev1.PodFailed {
					return false, fmt.Errorf("NUMA alignment pod failed: %s", pod.Status.Message)
				}
				return pod.Status.Phase == corev1.PodRunning, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(5*time.Second)); err != nil {
				t.Fatalf("NUMA alignment pod never reached Running: %v; observed status: %s", err, statusJSON(pod.Status))
			}
			return ctx
		}).
		Assess("allocated devices and CPUs share a NUMA node", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			topology, err := nodeNUMATopology(ctx, crClient, pod.Spec.NodeName)
			if err != nil {
				t.Fatal(err)
			}
			claims := &resourcev1.ResourceClaimList{}
			if err := crClient.List(ctx, claims, client.InNamespace(slurmBridgeNamespace)); err != nil {
				t.Fatal(err)
			}
			// List attributes match when they intersect, so the allocation is
			// aligned when one NUMA node is local to every device.
			var common []int64
			allocated := make(map[structured.DeviceID][]int64)
			for i := range claims.Items {
				claim := &claims.Items[i]
				if !metav1.IsControlledBy(claim, pod) || claim.Status.Allocation == nil {
					continue
				}
				for _, result := range claim.Status.Allocation.Devices.Results {
					id := structured.MakeDeviceID(result.Driver, result.Pool, result.Device)
					nodes, ok := topology.devices[id]
					if !ok {
						t.Fatalf("allocated device %s has no NUMA locality", id)
					}
					if len(allocated) == 0 {
						common = slices.Clone(nodes)
					}
					common = slices.DeleteFunc(common, func(node int64) bool { return !slices.Contains(nodes, node) })
					allocated[id] = nodes
				}
			}
			drivers := sets.New[string]()
			for id := range allocated {
				drivers.Insert(id.Driver.String())
			}
			if !drivers.HasAll(nodeinfo.DraDriverCpu, draNvidiaGPUDriver, dranetDriver) || len(common) == 0 {
				t.Fatalf("allocated devices do not share a NUMA node: %v", allocated)
			}
			cpus, err := verifySlurmCPUSet(ctx, config, crClient, pod)
			if err != nil {
				t.Fatal(err)
			}
			local := cpuset.New()
			for _, node := range common {
				local = local.Union(topology.cpus[node])
			}
			if !cpus.IsSubsetOf(local) {
				t.Fatalf("Slurm allocated CPUs %s outside NUMA nodes %v (CPUs %s): %v", cpus, common, local, allocated)
			}
			t.Logf("NUMA nodes %v hold CPUs %s and devices %v", common, cpus, allocated)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if t.Failed() {
				captureFailureDiagnostics(t, featureName,
					slurmBridgeNamespace, slurmNamespace, "kube-system", "dra-driver-nvidia-gpu", "nvml-mock")
			}
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("failed to get client for NUMA alignment cleanup: %v", err)
				return ctx
			}
			// The Pod must finish cleanup before its DeviceClass is deleted.
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			deleteObject(t, ctx, crClient, deviceClass)
			return ctx
		}).
		Feature()
}
