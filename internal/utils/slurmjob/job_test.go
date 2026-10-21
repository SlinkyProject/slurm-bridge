// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"testing"

	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
)

func TestHeldJob(t *testing.T) {
	for _, tt := range []struct {
		name     string
		state    api.V0044JobInfoJobState
		priority *api.V0044Uint32NoValStruct
		held     bool
	}{
		{"held", api.V0044JobInfoJobStatePENDING, &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(0))}, true},
		{"released", api.V0044JobInfoJobStatePENDING, &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(1))}, false},
		{"unset", api.V0044JobInfoJobStatePENDING, &api.V0044Uint32NoValStruct{}, false},
		{"absent", api.V0044JobInfoJobStatePENDING, nil, false},
		{"running", api.V0044JobInfoJobStateRUNNING, &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(0))}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job, err := FromJobInfo(&api.V0044JobInfo{JobId: ptr.To(int32(100)), JobState: &[]api.V0044JobInfoJobState{tt.state}, Priority: tt.priority})
			if err != nil || job.Held != tt.held {
				t.Fatalf("job=%+v, error=%v; want held=%v", job, err, tt.held)
			}
		})
	}
}
