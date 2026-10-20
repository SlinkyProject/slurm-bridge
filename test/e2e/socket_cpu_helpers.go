// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	api "github.com/SlinkyProject/slurm-client/api/v0044"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

func requireCPUDRA(t *testing.T, config *envconf.Config) {
	t.Helper()
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config.Client().RESTConfig())
	if err != nil {
		t.Fatalf("create Kubernetes discovery client: %v", err)
	}
	info, err := discoveryClient.ServerVersion()
	if err != nil {
		t.Fatalf("discover Kubernetes server version: %v", err)
	}
	serverVersion, err := utilversion.ParseGeneric(info.GitVersion)
	if err != nil {
		t.Fatalf("parse Kubernetes server version: %v", err)
	}
	if serverVersion.LessThan(utilversion.MustParseGeneric("1.36.0")) {
		t.Skipf("CPU DRA requires Kubernetes 1.36 or newer; server is %s", serverVersion)
	}
}

// slurmAllocatedCPUSet translates Slurm's bitmap using the registered topology.
// It queries Slurm independently of the claim and driver environment.
func slurmAllocatedCPUSet(ctx context.Context, config *envconf.Config, c client.Client, pod *corev1.Pod) (cpuset.CPUSet, error) {
	controller, err := getSlurmControllerPod(ctx, c)
	if err != nil {
		return cpuset.New(), err
	}
	node := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
		return cpuset.New(), err
	}
	nodeName := nodeutils.GetSlurmNodeName(node)
	output, err := execInPod(ctx, config, controller, "scontrol", "show", "resources", pod.Labels[slurmJobIDLabel], "--json=v0.0.44")
	if err != nil {
		return cpuset.New(), err
	}
	var layout api.V0044OpenapiResourceLayoutResp
	if err := json.Unmarshal([]byte(output), &layout); err != nil {
		return cpuset.New(), err
	}
	output, err = execInPod(ctx, config, controller, "scontrol", "show", "node", nodeName, "--json=v0.0.44")
	if err != nil {
		return cpuset.New(), err
	}
	var nodes api.V0044OpenapiNodesResp
	if err := json.Unmarshal([]byte(output), &nodes); err != nil {
		return cpuset.New(), err
	}
	for _, slurmNode := range nodes.Nodes {
		if ptr.Deref(slurmNode.Name, "") != nodeName {
			continue
		}
		current, err := nodeinfo.NewNodeInfo(ctx, c, node.Name)
		if err != nil {
			return cpuset.New(), err
		}
		if err := current.ValidateCPUFingerprint(ptr.Deref(slurmNode.Extra, "")); err != nil {
			return cpuset.New(), err
		}
		for _, allocation := range layout.Nodes {
			if allocation.Node != nodeName {
				continue
			}
			bitmap, err := bitmaputil.NewFrom(ptr.Deref(allocation.CoreBitmap, ""))
			if err != nil {
				return cpuset.New(), err
			}
			invalid := false
			bitmap.Range(func(core uint32) {
				if int(core) >= len(current.CpuMap.AbstractToMachine) {
					invalid = true
				}
			})
			cpus := current.CpuMap.ToMachineCPUs(bitmap)
			if invalid || cpus.IsEmpty() {
				return cpuset.New(), fmt.Errorf("invalid CPU bitmap returned by Slurm")
			}
			return cpus, nil
		}
	}
	return cpuset.New(), fmt.Errorf("no Slurm CPU allocation for node %q", nodeName)
}

func verifySlurmCPUSet(ctx context.Context, config *envconf.Config, c client.Client, pod *corev1.Pod) (cpuset.CPUSet, error) {
	expected, err := slurmAllocatedCPUSet(ctx, config, c, pod)
	if err != nil {
		return cpuset.New(), err
	}
	environment, err := execInPod(ctx, config, pod, "env")
	if err != nil {
		return cpuset.New(), err
	}
	allocated, err := draCPUSetFromEnvironment(environment)
	if err != nil {
		return cpuset.New(), err
	}
	output, err := execInPod(ctx, config, pod, "cat", "/sys/fs/cgroup/cpuset.cpus.effective")
	if err != nil {
		return cpuset.New(), err
	}
	effective, err := cpuset.Parse(strings.TrimSpace(output))
	if err != nil {
		return cpuset.New(), err
	}
	if !effective.Equals(expected) || !allocated.Equals(expected) {
		return cpuset.New(), fmt.Errorf("CPU sets differ: Slurm=%s DRA=%s effective=%s", expected.String(), allocated.String(), effective.String())
	}
	return expected, nil
}
