// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
)

// TerminateHetJobComponent implements SlurmControlInterface.
func (r *realSlurmControl) TerminateHetJobComponent(ctx context.Context, hetJobId, hetJobOffset int32) error {
	if hetJobId <= 0 || hetJobOffset < 0 {
		return fmt.Errorf("invalid heterogeneous job component %d+%d", hetJobId, hetJobOffset)
	}
	raw := r.Versioned().V0044()
	if raw == nil {
		return errors.New("raw Slurm v0.0.44 client is unavailable")
	}
	selector := fmt.Sprintf("%d+%d", hetJobId, hetJobOffset)
	res, err := raw.SlurmV0044DeleteJobWithResponse(ctx, selector, &api.SlurmV0044DeleteJobParams{},
		func(_ context.Context, req *http.Request) error {
			req.URL.RawPath = strings.ReplaceAll(req.URL.EscapedPath(), "+", "%2B")
			return nil
		})
	if err != nil {
		return fmt.Errorf("terminate heterogeneous job component %s: %w", selector, err)
	}
	if res == nil || res.HTTPResponse == nil {
		return fmt.Errorf("terminate heterogeneous job component %s: missing HTTP response", selector)
	}
	if res.StatusCode() == http.StatusNotFound {
		return nil // Already absent, matching TerminateJob's idempotent behavior.
	}
	var errs []error
	if res.StatusCode() != http.StatusOK {
		errs = append(errs, fmt.Errorf("HTTP %d: %s", res.StatusCode(), http.StatusText(res.StatusCode())))
	}
	body := res.JSON200
	if body == nil {
		body = res.JSONDefault
	}
	if body == nil {
		errs = append(errs, errors.New("missing JSON cancellation response"))
	} else {
		for _, apiErr := range ptr.Deref(body.Errors, []api.V0044OpenapiError{}) {
			errs = append(errs, fmt.Errorf("slurm error %d (%s): %s: %s",
				ptr.Deref(apiErr.ErrorNumber, 0), ptr.Deref(apiErr.Source, ""),
				ptr.Deref(apiErr.Error, ""), ptr.Deref(apiErr.Description, "")))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("terminate heterogeneous job component %s: %w", selector, err)
	}
	return nil
}
