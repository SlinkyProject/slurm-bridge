// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package wellknown

const (
	// AnnotationExternalJobNode indicates the Node which corresponds to the
	// the pod's external job.
	AnnotationExternalJobNode = SlinkyPrefix + "slurm-node"

	// AnnotationExternalNodePartitions specifies the Slurm partition(s) that this
	// Kubernetes node should be added to. The annotation value is a comma-separated
	// list of partition names. The node will be registered with node features
	// matching these partition names. The controller will error if any partition
	// does not exist.
	AnnotationExternalNodePartitions = SchedulerPrefix + "external-node-partitions"
)

const (
	// AnnotationAccount overrides the default account
	// for the Slurm external job.
	AnnotationAccount = SlurmJobPrefix + "account"
	// AnnotationConstraint sets the constraint
	// for the Slurm external job.
	AnnotationConstraints = SlurmJobPrefix + "constraints"
	// AnnotationCpuPerTask sets the number of cpus
	// per task
	AnnotationCpuPerTask = SlurmJobPrefix + "cpu-per-task"
	// AnnotationExclusive overrides the default exclusive (SharedNone)
	// flag for the Slurm external job. Set to "false" for non-exclusive
	// placement; any other value or unset keeps exclusive.
	AnnotationExclusive = SlurmJobPrefix + "exclusive"
	// AnnotationGres overrides the default gres
	// for the Slurm external job.
	AnnotationGres = SlurmJobPrefix + "gres"
	// AnnotationGroupId overrides the default groupid
	// for the Slurm external job.
	AnnotationGroupId = SlurmJobPrefix + "group-id"
	// AnnotationJobName sets the job name for
	// the slurm job
	AnnotationJobName = SlurmJobPrefix + "job-name"
	// AnnotationLicenses sets the licenses
	// for the Slurm external job.
	AnnotationLicenses = SlurmJobPrefix + "licenses"
	// AnnotationMaxNodes sets the maximum number of
	// nodes for the external job
	AnnotationMaxNodes = SlurmJobPrefix + "max-nodes"
	// AnnotationMemPerNode sets the amount of memory
	// per node
	AnnotationMemPerNode = SlurmJobPrefix + "mem-per-node"
	// AnnotationMinNodes sets the minimum number of
	// nodes for the external job
	AnnotationMinNodes = SlurmJobPrefix + "min-nodes"
	// AnnotationPartitions overrides the default partition
	// for the Slurm external job.
	AnnotationPartition = SlurmJobPrefix + "partition"
	// AnnotationQOS overrides the default QOS
	// for the Slurm external job.
	AnnotationQOS = SlurmJobPrefix + "qos"
	// AnnotationReservation sets the reservation
	// for the Slurm external job.
	AnnotationReservation = SlurmJobPrefix + "reservation"
	// AnnotationTimelimit sets the Time Limit in minutes
	// for the Slurm external job.
	AnnotationTimeLimit = SlurmJobPrefix + "timelimit"
	// AnnotationUserId overrides the default userid
	// for the Slurm external job.
	AnnotationUserId = SlurmJobPrefix + "user-id"
	// AnnotationWckey sets the Wckey
	// for the Slurm external job.
	AnnotationWckey = SlurmJobPrefix + "wckey"
)
