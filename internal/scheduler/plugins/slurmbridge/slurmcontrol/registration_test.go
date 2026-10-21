// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func TestRegisterJob(t *testing.T) {
	for _, tc := range []struct {
		name               string
		components         int
		invalidDescription bool
		responseID         *int32
		createErr          error
		wantID             int32
		wantErr            bool
		notSubmitted       bool
	}{
		{name: "single held job", components: 1, responseID: ptr.To(int32(42)), wantID: 42},
		{name: "held heterogeneous job", components: 2, responseID: ptr.To(int32(42)), wantID: 42},
		{name: "invalid IR", wantErr: true, notSubmitted: true},
		{name: "invalid second description", components: 2, invalidDescription: true, wantErr: true, notSubmitted: true},
		{name: "transport error", components: 1, createErr: context.DeadlineExceeded, wantErr: true},
		{name: "transport error with accepted ID", components: 1, responseID: ptr.To(int32(42)), createErr: context.DeadlineExceeded, wantID: 42, wantErr: true},
		{name: "missing response ID", components: 1, wantErr: true},
		{name: "zero response ID", components: 1, responseID: ptr.To(int32(0)), wantErr: true},
		{name: "negative response ID", components: 1, responseID: ptr.To(int32(-1)), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ir := &slurmjobir.SlurmJobIR{}
			for i := range tc.components {
				ir.Components = append(ir.Components, slurmjobir.SlurmJobComponent{
					Pods:    corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("pod-%d", i)}}}},
					JobInfo: slurmjobir.SlurmJobIRJobInfo{Hold: ptr.To(false)},
				})
			}
			if tc.invalidDescription {
				ir.Components[1].JobInfo.Constraints = ptr.To("(a&b)|(c&d)")
			}
			calls := 0
			registrar := NewRegistrar(fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, obj object.Object, request any, opts ...client.CreateOption) error {
					calls++
					if !(&client.CreateOptions{}).ApplyOptions(opts).SkipReadAfterCreate {
						t.Error("submission must return without reading the informer cache")
					}
					req := request.(api.V0044JobSubmitReq)
					var descriptions []api.V0044JobDescMsg
					if tc.components == 1 {
						if req.Job == nil || req.Jobs != nil {
							t.Fatal("single submission must use Job only")
						}
						descriptions = []api.V0044JobDescMsg{*req.Job}
					} else {
						if req.Job != nil || req.Jobs == nil || len(*req.Jobs) != tc.components {
							t.Fatal("heterogeneous submission must include every component in Jobs")
						}
						descriptions = *req.Jobs
					}
					for _, desc := range descriptions {
						if !ptr.Deref(desc.Hold, false) {
							t.Error("every submitted component must be held")
						}
					}
					obj.(*api.V0044JobInfo).JobId = tc.responseID
					return tc.createErr
				},
			}).Build(), "", "")
			id, err := registrar.RegisterJob(t.Context(), ir)
			if id != tc.wantID || (err != nil) != tc.wantErr {
				t.Fatalf("RegisterJob = (%d, %v), want ID %d, error %t", id, err, tc.wantID, tc.wantErr)
			}
			wantCalls := 1
			if tc.notSubmitted {
				wantCalls = 0
			}
			if calls != wantCalls || errors.Is(err, ErrRegistrationNotSubmitted) != tc.notSubmitted {
				t.Fatalf("submission boundary: calls=%d, error=%v; want calls=%d, notSubmitted=%t", calls, err, wantCalls, tc.notSubmitted)
			}
			if tc.createErr != nil && !errors.Is(err, tc.createErr) {
				t.Fatalf("lost create error: %v", err)
			}
		})
	}
}
