// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package wellknown

const (
	// AnnotationPlaceholderNode indicates the Node which corresponds to the
	// the pod's placeholder job.
	AnnotationPlaceholderNode = "slinky.slurm.net/slurm-node"

	// AnnotationAccount overrides the default account
	// for the Slurm placeholder job.
	AnnotationAccount = "slinky.slurm.net/account"
	// AnnotationConstraint sets the constraint
	// for the Slurm placeholder job.
	AnnotationConstraints = "slinky.slurm.net/constraints"
	// AnnotationCpuPerTask sets the number of cpus
	// per task
	AnnotationCpuPerTask = "slinky.slurm.net/cpu-per-task"
	// AnnotationGroupId overrides the default groupid
	// for the Slurm placeholder job.
	AnnotationGroupId = "slinky.slurm.net/group-id"
	// AnnotationJobName sets the job name for
	// the slurm job
	AnnotationJobName = "slinky.slurm.net/job-name"
	// AnnotationLicenses sets the licenses
	// for the Slurm placeholder job.
	AnnotationLicenses = "slinky.slurm.net/licenses"
	// AnnotationMaxNodes sets the maximum number of
	// nodes for the placeholder job
	AnnotationMaxNodes = "slinky.slurm.net/max-nodes"
	// AnnotationMemPerNode sets the amount of memory
	// per node
	AnnotationMemPerNode = "slinky.slurm.net/mem-per-node"
	// AnnotationMinNodes sets the minimum number of
	// nodes for the placeholder job
	AnnotationMinNodes = "slinky.slurm.net/min-nodes"
	// AnnotationPartitions overrides the default partition
	// for the Slurm placeholder job.
	AnnotationPartition = "slinky.slurm.net/partition"
	// AnnotationQOS overrides the default QOS
	// for the Slurm placeholder job.
	AnnotationQOS = "slinky.slurm.net/qos"
	// AnnotationReservation sets the reservation
	// for the Slurm placeholder job.
	AnnotationReservation = "slinky.slurm.net/reservation"
	// AnnotationTimelimit sets the Time Limit in minutes
	// for the Slurm placeholder job.
	AnnotationTimeLimit = "slinky.slurm.net/timelimit"
	// AnnotationUserId overrides the default userid
	// for the Slurm placeholder job.
	AnnotationUserId = "slinky.slurm.net/user-id"
	// AnnotationWckey sets the Wckey
	// for the Slurm placeholder job.
	AnnotationWckey = "slinky.slurm.net/wckey"
)
