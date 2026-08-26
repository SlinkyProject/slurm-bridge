// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

var testEnv = env.New()

func TestScheduling(t *testing.T) {
	nodeMode, err := parseSlurmNodeModeFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}

	testFeatures := []types.Feature{
		testSlurmBridgeReadiness(nodeMode),
		testSlurmBridgeJobScheduling(),
		testSlurmBridgePodScheduling(),
		testSlurmBridgeDRAResourceScheduling(false),
		testSlurmBridgeNvidiaGPUResourceScheduling(),
	}
	if nodeMode == slurmNodeModeExternal {
		testFeatures = append(testFeatures, testSlurmBridgeDRAResourceScheduling(true))
	} else {
		testFeatures = append(testFeatures, testHybridSlurmBatchScheduling())
	}

	_ = testEnv.Test(t, testFeatures...)
}
