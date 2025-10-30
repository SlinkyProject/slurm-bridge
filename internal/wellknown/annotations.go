// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package wellknown

const (
	// AnnotationPlaceholderNode indicates the Node which corresponds to the
	// the pod's placeholder job.
	AnnotationPlaceholderNode = SlinkyPrefix + "slurm-node"

	// AnnotationAccount overrides the default account
	// for the Slurm placeholder job.
	AnnotationAccount = SlinkyPrefix + "account"
	// AnnotationConstraint sets the constraint
	// for the Slurm placeholder job.
	AnnotationConstraints = SlinkyPrefix + "constraints"
	// AnnotationCpuPerTask sets the number of cpus
	// per task
	AnnotationCpuPerTask = SlinkyPrefix + "cpu-per-task"
	// AnnotationGres overrides the default gres
	// for the Slurm placeholder job.
	AnnotationGres = SlinkyPrefix + "gres"
	// AnnotationGroupId overrides the default groupid
	// for the Slurm placeholder job.
	AnnotationGroupId = SlinkyPrefix + "group-id"
	// AnnotationJobName sets the job name for
	// the slurm job
	AnnotationJobName = SlinkyPrefix + "job-name"
	// AnnotationLicenses sets the licenses
	// for the Slurm placeholder job.
	AnnotationLicenses = SlinkyPrefix + "licenses"
	// AnnotationMaxNodes sets the maximum number of
	// nodes for the placeholder job
	AnnotationMaxNodes = SlinkyPrefix + "max-nodes"
	// AnnotationMemPerNode sets the amount of memory
	// per node
	AnnotationMemPerNode = SlinkyPrefix + "mem-per-node"
	// AnnotationMinNodes sets the minimum number of
	// nodes for the placeholder job
	AnnotationMinNodes = SlinkyPrefix + "min-nodes"
	// AnnotationPartitions overrides the default partition
	// for the Slurm placeholder job.
	AnnotationPartition = SlinkyPrefix + "partition"
	// AnnotationQOS overrides the default QOS
	// for the Slurm placeholder job.
	AnnotationQOS = SlinkyPrefix + "qos"
	// AnnotationReservation sets the reservation
	// for the Slurm placeholder job.
	AnnotationReservation = SlinkyPrefix + "reservation"
	// AnnotationTimelimit sets the Time Limit in minutes
	// for the Slurm placeholder job.
	AnnotationTimeLimit = SlinkyPrefix + "timelimit"
	// AnnotationUserId overrides the default userid
	// for the Slurm placeholder job.
	AnnotationUserId = SlinkyPrefix + "user-id"
	// AnnotationWckey sets the Wckey
	// for the Slurm placeholder job.
	AnnotationWckey = SlinkyPrefix + "wckey"
)
