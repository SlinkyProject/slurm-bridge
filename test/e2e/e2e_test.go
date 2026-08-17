// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
)

var testEnv = env.New()

func TestScheduling(t *testing.T) {
	_ = testEnv.Test(t,
		testSlurmBridgeJobScheduling(),
		testSlurmBridgePodScheduling(),
	)
}
