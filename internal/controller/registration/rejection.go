// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

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

var (
	requestErrors = sets.New[int32](
		slurmerrors.ErrInvalidPartitionName,
		slurmerrors.ErrInvalidNodeCount,
		slurmerrors.ErrBadTaskCount,
		slurmerrors.ErrInvalidFeature,
		slurmerrors.ErrInvalidAccount,
		slurmerrors.ErrInvalidLicenses,
		slurmerrors.ErrInvalidTimeLimit,
		slurmerrors.ErrInvalidWCKey,
		slurmerrors.ErrInvalidQOS,
		slurmerrors.ErrInvalidGRES,
		slurmerrors.ErrInvalidTRES,
		slurmerrors.ErrUserIDInvalid,
		slurmerrors.ErrUserIDUnknown,
	)
	// ErrAccountingPolicy covers submit limits and some association errors.
	limitErrors = sets.New[int32](
		slurmerrors.ErrMaxJobCount,
		slurmerrors.ErrAccountingPolicy,
		slurmerrors.ErrPartitionNotAvail,
		slurmerrors.ErrPartitionDown,
	)
)

func classifyRejection(err error) rejection {
	var se *slurmerrors.SlurmError
	switch {
	case !errors.As(err, &se):
		return rejectOther
	case requestErrors.Has(se.Number):
		return rejectRequest
	case limitErrors.Has(se.Number):
		return rejectLimit
	}
	return rejectOther
}
