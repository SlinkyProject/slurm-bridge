// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"fmt"
	"slices"
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"
)

func Test_realSlurmControl_RefreshNodeCache(t *testing.T) {
	tests := []struct {
		name        string
		slurmClient client.Client
		wantErr     bool
	}{
		{
			name:        "empty",
			slurmClient: fake.NewFakeClient(),
		},
		{
			name: "jobs",
			slurmClient: fake.NewClientBuilder().
				WithLists(&types.V0044NodeList{
					Items: []types.V0044Node{
						{
							V0044Node: api.V0044Node{
								Name: ptr.To("node-0"),
							},
						},
						{
							V0044Node: api.V0044Node{
								Name: ptr.To("node-1"),
							},
						},
					},
				}).
				Build(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewControl(tt.slurmClient)
			gotErr := r.RefreshNodeCache(context.Background())
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("RefreshNodeCache() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("RefreshNodeCache() succeeded unexpectedly")
			}
		})
	}
}

func Test_realSlurmControl_ListNodeNames(t *testing.T) {
	ctx := context.Background()
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "Empty",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx: ctx,
			},
			want:    []string{},
			wantErr: false,
		},
		{
			name: "Not empty",
			fields: fields{
				Client: func() client.Client {
					list := &types.V0044NodeList{
						Items: []types.V0044Node{
							{V0044Node: api.V0044Node{Name: ptr.To("node-0")}},
							{V0044Node: api.V0044Node{Name: ptr.To("node-1")}},
						},
					}
					c := fake.NewClientBuilder().
						WithLists(list).
						Build()
					return c
				}(),
			},
			args: args{
				ctx: ctx,
			},
			want:    []string{"node-0", "node-1"},
			wantErr: false,
		},
		{
			name: "Failure",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						List: func(ctx context.Context, list object.ObjectList, opts ...client.ListOption) error {
							return fmt.Errorf("failed to list resources")
						},
					}
					c := fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
					return c
				}(),
			},
			args: args{
				ctx: ctx,
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			got, err := r.ListNodeNames(tt.args.ctx)
			slices.Sort(got)
			slices.Sort(tt.want)
			if (err != nil) != tt.wantErr {
				t.Errorf("ListNodeNames() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("ListNodeNames() = %v, want %v", got, tt.want)
			}
		})
	}
}
