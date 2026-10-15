// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/types"
)

func TestScheduling(t *testing.T) {
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	nodeMode, err := parseSlurmNodeModeFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	requireNvidiaGPU, err := parseMockNVMLFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseE2ECleanupFromEnvironment(); err != nil {
		t.Fatal(err)
	}

	_ = testEnv.Test(t, testSlurmBridgeReadiness(nodeMode))

	testFeatures := []types.Feature{
		testAdmissionRoutingBoundaries(),
		testExclusiveCPUDRARejected(),
		testSlurmBridgeJobScheduling(),
		testSlurmBridgeParallelJobScheduling(),
		testSlurmBridgeSequentialJobScheduling(),
		testSlurmBridgeJobSetScheduling(),
		testSchedulerPluginsPodGroupScheduling(),
		testLeaderWorkerSetScheduling(),
		testSlurmJobRoundTrip(),
		testKubernetesCancellation(),
		testSlurmCancellation(),
		testSlurmBridgePodScheduling(),
		testSlurmBridgeDRAResourceScheduling(false),
		testSlurmBridgeNvidiaGPUResourceScheduling(requireNvidiaGPU),
		testSlurmBridgeDRANETResourceScheduling(),
	}
	for _, api := range []kubernetesPodGroupAPI{podGroupV1Beta1, podGroupV1Alpha2} {
		testFeatures = append(testFeatures,
			testKubernetesPodGroupScheduling(api),
			testSlurmBridgeJobSetPodGroupScheduling(api),
			testLeaderWorkerSetPodGroupScheduling(api),
		)
	}
	if nodeMode == slurmNodeModeHybrid {
		testFeatures = append(testFeatures, testHybridSlurmBatchScheduling())
	}

	_ = testEnv.TestInParallel(t, testFeatures...)
	if nodeMode == slurmNodeModeHybrid {
		// MCS isolation needs a native allocation with spare resources, so run
		// after the other scheduling features have released their allocations.
		_ = testEnv.Test(t, testHybridMCSIsolation())
		// Changing GRES compatibility would disrupt the scheduling features above.
		_ = testEnv.Test(t, testHybridGRESCompatibilityCondition())
	}
}

// TestSocketCPUAllocation runs the focused CPU/GPU runtime and reuse checks.
func TestSocketCPUAllocation(t *testing.T) {
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseE2ECleanupFromEnvironment(); err != nil {
		t.Fatal(err)
	}
	_ = testEnv.Test(t, testExclusiveCPUDRARejected())
	_ = testEnv.Test(t, testSlurmBridgeDRAResourceScheduling(true))
}
