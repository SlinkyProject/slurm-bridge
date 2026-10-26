// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"errors"
	"time"

	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
)

// rejection classifies a Slurm submission error by its Slurm error number.
type rejection int

const (
	// rejectOther covers unknown errors and unreachable Slurm; retry with backoff.
	rejectOther rejection = iota
	// rejectRequest means Slurm refused the request against its current config.
	rejectRequest
	// rejectLimit means a limit or capacity condition that may clear; retry soon.
	rejectLimit
)

const (
	// Slurm validates most requests against its config, so an admin change can
	// make a refused request valid without any change to the workload.
	requestRetry = 10 * time.Minute
	// limitRetry spaces retries for limit rejections so a full queue is not polled.
	limitRetry = time.Minute
)

// Slurm error numbers from slurm_errno.h.
var (
	requestErrors = map[int32]bool{
		2000: true, // ESLURM_INVALID_PARTITION_NAME
		2006: true, // ESLURM_INVALID_NODE_COUNT
		2025: true, // ESLURM_BAD_TASK_COUNT
		2029: true, // ESLURM_INVALID_FEATURE
		2045: true, // ESLURM_INVALID_ACCOUNT
		2048: true, // ESLURM_INVALID_LICENSES
		2051: true, // ESLURM_INVALID_TIME_LIMIT
		2057: true, // ESLURM_INVALID_WCKEY
		2066: true, // ESLURM_INVALID_QOS
		2072: true, // ESLURM_INVALID_GRES
		2115: true, // ESLURM_INVALID_TRES
		2165: true, // ESLURM_USER_ID_INVALID
		2166: true, // ESLURM_USER_ID_UNKNOWN
	}
	// ESLURM_ACCOUNTING_POLICY covers submit limits and some association errors.
	limitErrors = map[int32]bool{
		2007: true, // ESLURM_MAX_JOB_COUNT
		2050: true, // ESLURM_ACCOUNTING_POLICY
		2070: true, // ESLURM_PARTITION_NOT_AVAIL
		2101: true, // ESLURM_PARTITION_DOWN
	}
)

func classifyRejection(err error) rejection {
	var se *slurmerrors.SlurmError
	switch {
	case !errors.As(err, &se):
		return rejectOther
	case requestErrors[se.Number]:
		return rejectRequest
	case limitErrors[se.Number]:
		return rejectLimit
	}
	return rejectOther
}
