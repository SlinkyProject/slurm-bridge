// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const allocationIdentityAnnotation = "slinky.slurm.net/allocation-identity"

func allocationClaimName(pod *corev1.Pod) string {
	hash := sha256.Sum256([]byte(string(pod.UID) + "/" + pod.Labels[wellknown.LabelExternalJobId]))
	return "slurm-bridge-" + hex.EncodeToString(hash[:16])
}

func allocationIdentity(pod *corev1.Pod, node string, resources *slurmcontrol.NodeResources) string {
	// Unlike the claim name, this identity must detect changes to the allocation.
	// Exclude NodeExtra because inventory can change during a retry. Pending claims
	// revalidate it before binding; allocated claims retain their devices and CPU sets.
	if resources != nil {
		allocation := *resources
		allocation.NodeExtra = ""
		resources = &allocation
	}
	raw, _ := json.Marshal(struct {
		Pod       string
		Job       string
		Node      string
		Resources *slurmcontrol.NodeResources
	}{string(pod.UID), pod.Labels[wellknown.LabelExternalJobId], node, resources})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func validateClaimIdentity(claim *resourcev1.ResourceClaim, pod *corev1.Pod, node string, resources *slurmcontrol.NodeResources) error {
	if claim.DeletionTimestamp != nil {
		return fmt.Errorf("allocation claim %q is still being cleaned up", claim.Name)
	}
	owned := slices.ContainsFunc(claim.OwnerReferences, func(ref metav1.OwnerReference) bool {
		return ref.Kind == "Pod" && ref.UID == pod.UID && ref.Name == pod.Name && ptr.Deref(ref.Controller, false)
	})
	if !owned || claim.Annotations[allocationIdentityAnnotation] != allocationIdentity(pod, node, resources) {
		return fmt.Errorf("claim %q belongs to a different Slurm allocation; coordinated cleanup is required", claim.Name)
	}
	if claim.Status.Allocation != nil {
		if len(claim.Status.ReservedFor) != 1 || claim.Status.ReservedFor[0].UID != pod.UID {
			return fmt.Errorf("claim %q has an unexpected consumer reservation", claim.Name)
		}
		want := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}}}}}
		if !equality.Semantic.DeepEqual(claim.Status.Allocation.NodeSelector, want) {
			return fmt.Errorf("claim %q is allocated to a different node", claim.Name)
		}
	}
	return nil
}

func validateCPUResults(allocation *nodeinfo.CPUAllocation, results []resourcev1.DeviceRequestAllocationResult) error {
	expected := make(map[string]resourcev1.DeviceRequestAllocationResult)
	for _, result := range allocation.Results {
		expected[result.Request] = result
	}
	for _, result := range results {
		if result.Driver != nodeinfo.DraDriverCpu {
			continue
		}
		want, ok := expected[result.Request]
		if !ok || !equality.Semantic.DeepEqual(want, result) {
			return fmt.Errorf("CPU socket allocation %q has incorrect device, share ID or consumed capacity", result.Request)
		}
		delete(expected, result.Request)
	}
	if len(expected) != 0 {
		return fmt.Errorf("CPU socket allocation results are missing")
	}
	return nil
}
