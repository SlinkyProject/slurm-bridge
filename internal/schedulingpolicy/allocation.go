// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"fmt"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

// ValidateAllocation checks every member and each component before registration
// or release. It does not read or modify Kubernetes or Slurm state.
func ValidateAllocation(ir *slurmjobir.SlurmJobIR) error {
	for _, component := range ir.Components {
		for _, member := range component.Pods.Items {
			if err := PodError(&member, ValidatePod(&member)); err != nil {
				return fmt.Errorf("allocation member %s/%s: %w", member.Namespace, member.Name, err)
			}
		}
		if err := ValidateComponent(component.Pods.Items); err != nil {
			return err
		}
	}
	return nil
}
