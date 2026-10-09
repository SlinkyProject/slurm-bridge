// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjob

import (
	"context"
	"errors"
	"testing"

	"k8s.io/utils/ptr"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/runnable/slurmjob/slurmcontrol"
)

type terminationControl struct {
	slurmcontrol.SlurmControlInterface
	jobID  int32
	leader int32
	offset int32
	err    error
}

func (c *terminationControl) TerminateJob(_ context.Context, id int32) error {
	c.jobID = id
	return c.err
}

func (c *terminationControl) TerminateHetJobComponent(_ context.Context, leader, offset int32) error {
	c.leader, c.offset = leader, offset
	return c.err
}

func TestCleanDanglingJobTermination(t *testing.T) {
	failure := errors.New("cancellation failed")
	for _, tt := range []struct {
		name    string
		leader  int32
		offset  *int32
		err     error
		wantErr bool
	}{
		{name: "homogeneous job"},
		{name: "leader component", leader: 93, offset: ptr.To[int32](0)},
		{name: "non-leader component", leader: 93, offset: ptr.To[int32](2)},
		{name: "missing offset", leader: 93, wantErr: true},
		{name: "component cancellation error", leader: 93, offset: ptr.To[int32](2), err: failure, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job := &types.V0044JobInfo{V0044JobInfo: api.V0044JobInfo{
				JobId:    ptr.To[int32](95),
				JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
			}}
			if tt.leader != 0 {
				job.HetJobId = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(tt.leader)}
			}
			if tt.offset != nil {
				job.HetJobOffset = &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: tt.offset}
			}
			cl := fake.NewClientBuilder().WithObjects(job).Build()
			r := NewRunnable(kubefake.NewClientBuilder().Build(), cl, nil)
			control := &terminationControl{SlurmControlInterface: r.slurmControl, err: tt.err}
			r.slurmControl = control
			err := r.cleanDanglingJob(context.Background(), 95)
			if (err != nil) != tt.wantErr {
				t.Errorf("cleanDanglingJob(95) error = %v, wantErr %t", err, tt.wantErr)
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Errorf("error = %v, want wrapped %v", err, tt.err)
			}
			switch {
			case tt.leader == 0:
				if control.jobID != 95 || control.leader != 0 {
					t.Errorf("termination = %+v, want ordinary job 95", control)
				}
			case tt.offset != nil:
				if control.jobID != 0 || control.leader != tt.leader || control.offset != *tt.offset {
					t.Errorf("termination = %+v, want component %d+%d", control, tt.leader, *tt.offset)
				}
			case control.jobID != 0 || control.leader != 0:
				t.Error("missing offset must not cancel a job")
			}
		})
	}
}
