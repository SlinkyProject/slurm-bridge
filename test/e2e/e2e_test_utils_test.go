// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"strings"
	"testing"

	"k8s.io/utils/cpuset"
)

func TestDRACPUSetFromEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment string
		want        cpuset.CPUSet
		wantErr     string
	}{
		{
			name:        "exclusive allocation",
			environment: "PATH=/bin\nDRA_CPUSET_claim=0-7\n",
			want:        cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
		},
		{
			name:        "non-contiguous allocation",
			environment: "DRA_CPUSET_claim=0-2,6\n",
			want:        cpuset.New(0, 1, 2, 6),
		},
		{
			name:        "missing allocation",
			environment: "PATH=/bin\n",
			wantErr:     "was not injected",
		},
		{
			name:        "multiple allocations",
			environment: "DRA_CPUSET_first=0\nDRA_CPUSET_second=1\n",
			wantErr:     "multiple DRA CPU allocations",
		},
		{
			name:        "invalid allocation",
			environment: "DRA_CPUSET_claim=not-a-cpuset\n",
			wantErr:     "parse allocated CPU set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := draCPUSetFromEnvironment(tt.environment)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("draCPUSetFromEnvironment() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("draCPUSetFromEnvironment() error = %v", err)
			}
			if !got.Equals(tt.want) {
				t.Fatalf("draCPUSetFromEnvironment() = %s, want %s", got.String(), tt.want.String())
			}
		})
	}
}
