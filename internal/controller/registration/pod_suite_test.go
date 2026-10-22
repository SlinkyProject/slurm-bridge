// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const schedulerName = "slurm-bridge-scheduler"

var ctx = context.Background()

func TestPodLifecycle(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Allocation Pod lifecycle")
}
