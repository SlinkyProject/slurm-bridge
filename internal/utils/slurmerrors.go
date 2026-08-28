// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"errors"
	"net/http"
	"strings"

	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
)

// IsSlurmJobNotFoundErr reports whether err means the requested Slurm job
// does not exist. Cached reads return the typed ErrObjectNotFound; direct
// API reads return an aggregate whose elements are the HTTP status text
// (e.g. "Not Found") and slurmrestd's own messages (e.g. "Invalid job id
// specified"). Elements are compared whole, never by substring, so an
// unrelated error that merely contains "not found" is not misclassified
// as a missing job.
func IsSlurmJobNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, slurmerrors.ErrObjectNotFound) {
		return true
	}
	found := false
	walkErrors(err, func(e error) {
		// Compare whole lines, never substrings: aggregates carry one
		// message per element, but some paths flatten them into a single
		// newline-joined string.
		for _, line := range strings.Split(e.Error(), "\n") {
			line = strings.TrimSpace(line)
			if line == http.StatusText(http.StatusNotFound) ||
				strings.EqualFold(line, "invalid job id specified") {
				found = true
			}
		}
	})
	return found
}

// walkErrors visits err and every error reachable through Unwrap.
func walkErrors(err error, visit func(error)) {
	if err == nil {
		return
	}
	visit(err)
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			walkErrors(child, visit)
		}
		return
	}
	if agg, ok := err.(interface{ Errors() []error }); ok {
		for _, child := range agg.Errors() {
			walkErrors(child, visit)
		}
		return
	}
	walkErrors(errors.Unwrap(err), visit)
}
