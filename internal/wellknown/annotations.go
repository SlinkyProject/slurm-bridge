// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package wellknown

import (
	"fmt"
	"slices"
)

const (
	// AnnotationPlaceholderNode indicates the Node which corresponds to the
	// the pod's placeholder job.
	AnnotationPlaceholderNode = SlinkyPrefix + "slurm-node"
)

const (
	// AnnotationAccount overrides the default account
	// for the Slurm placeholder job.
	AnnotationAccount = SlurmJobPrefix + "account"
	// AnnotationConstraint sets the constraint
	// for the Slurm placeholder job.
	AnnotationConstraints = SlurmJobPrefix + "constraints"
	// AnnotationCpuPerTask sets the number of cpus
	// per task
	AnnotationCpuPerTask = SlurmJobPrefix + "cpu-per-task"
	// AnnotationGres overrides the default gres
	// for the Slurm placeholder job.
	AnnotationGres = SlurmJobPrefix + "gres"
	// AnnotationGroupId overrides the default groupid
	// for the Slurm placeholder job.
	AnnotationGroupId = SlurmJobPrefix + "group-id"
	// AnnotationJobName sets the job name for
	// the slurm job
	AnnotationJobName = SlurmJobPrefix + "job-name"
	// AnnotationLicenses sets the licenses
	// for the Slurm placeholder job.
	AnnotationLicenses = SlurmJobPrefix + "licenses"
	// AnnotationMaxNodes sets the maximum number of
	// nodes for the placeholder job
	AnnotationMaxNodes = SlurmJobPrefix + "max-nodes"
	// AnnotationMemPerNode sets the amount of memory
	// per node
	AnnotationMemPerNode = SlurmJobPrefix + "mem-per-node"
	// AnnotationMinNodes sets the minimum number of
	// nodes for the placeholder job
	AnnotationMinNodes = SlurmJobPrefix + "min-nodes"
	// AnnotationPartitions overrides the default partition
	// for the Slurm placeholder job.
	AnnotationPartition = SlurmJobPrefix + "partition"
	// AnnotationQOS overrides the default QOS
	// for the Slurm placeholder job.
	AnnotationQOS = SlurmJobPrefix + "qos"
	// AnnotationReservation sets the reservation
	// for the Slurm placeholder job.
	AnnotationReservation = SlurmJobPrefix + "reservation"
	// AnnotationTimelimit sets the Time Limit in minutes
	// for the Slurm placeholder job.
	AnnotationTimeLimit = SlurmJobPrefix + "timelimit"
	// AnnotationUserId overrides the default userid
	// for the Slurm placeholder job.
	AnnotationUserId = SlurmJobPrefix + "user-id"
	// AnnotationWckey sets the Wckey
	// for the Slurm placeholder job.
	AnnotationWckey = SlurmJobPrefix + "wckey"
	// AnnotationShared sets the shared policy
	// for the Slurm placeholder job.
	AnnotationShared = SlurmJobPrefix + "shared"
)

// SharedAllowedValues are the allowed values for the shared annotation
// (V0044JobDescMsgShared in slurm-client).
var SharedAllowedValues = []string{"none", "user"}

// ValidateSharedValue returns true if v is one of SharedAllowedValues.
func ValidateSharedValue(v string) error {
	if slices.Contains(SharedAllowedValues, v) {
		return nil
	}
	return fmt.Errorf("shared annotation value must be one of: none, user")
}
