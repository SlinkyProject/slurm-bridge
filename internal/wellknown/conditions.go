// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package wellknown

import corev1 "k8s.io/api/core/v1"

const (
	// NodeConditionSlurmGRESCompatible reports whether a hybrid node's Slurm
	// GRES configuration can represent its Kubernetes DRA inventory.
	NodeConditionSlurmGRESCompatible corev1.NodeConditionType = "SlinkySlurmGRESCompatible"
)
