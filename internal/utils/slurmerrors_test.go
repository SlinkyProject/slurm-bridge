// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"errors"
	"fmt"
	"testing"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
)

func TestIsSlurmJobNotFoundErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "typed cache error", err: slurmerrors.ErrObjectNotFound, want: true},
		{name: "wrapped typed error", err: fmt.Errorf("get job: %w", slurmerrors.ErrObjectNotFound), want: true},
		{name: "plain 404 status text", err: errors.New("Not Found"), want: true},
		{
			// The shape slurm-client's direct reads produce.
			name: "direct-read aggregate 404",
			err:  utilerrors.NewAggregate([]error{errors.New("Not Found"), errors.New("Invalid job id specified")}),
			want: true,
		},
		{name: "slurmctld invalid job id alone", err: errors.New("Invalid job id specified"), want: true},
		{name: "flattened newline-joined form", err: errors.New("Not Found\nInvalid job id specified"), want: true},
		{name: "unrelated error", err: errors.New("connection refused"), want: false},
		{
			// A substring match must not classify an unrelated error.
			name: "substring not found in transient error",
			err:  errors.New("dial tcp: lookup slurm-restapi: host not found"),
			want: false,
		},
		{
			name: "aggregate 5xx with unrelated message",
			err:  utilerrors.NewAggregate([]error{errors.New("Internal Server Error"), errors.New("upstream not found")}),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSlurmJobNotFoundErr(tt.err); got != tt.want {
				t.Errorf("IsSlurmJobNotFoundErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
