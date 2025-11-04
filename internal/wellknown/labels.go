// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package wellknown

const (
	// LabelSlurmNodeName indicates the Slurm NodeName which corresponds to the
	// labeled Kubernetes node.
	LabelSlurmNodeName = SlinkyPrefix + "slurm-nodename"

	// LabelPlaceholderJobId indicates the Slurm JobId which corresponds to the
	// the pod's placeholder job.
	LabelPlaceholderJobId = SchedulerPrefix + "slurm-jobid"
)
