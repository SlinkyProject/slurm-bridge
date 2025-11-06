// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v0044 "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	"github.com/SlinkyProject/slurm-client/pkg/types"
)

func Test_realSlurmControl_GetNodeNames(t *testing.T) {
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
							{V0044Node: v0044.V0044Node{Name: ptr.To("node-0")}},
							{V0044Node: v0044.V0044Node{Name: ptr.To("node-1")}},
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
			got, err := r.GetNodeNames(tt.args.ctx)
			slices.Sort(got)
			slices.Sort(tt.want)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.GetNodeNames() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetNodeNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_MakeNodeDrain(t *testing.T) {
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx    context.Context
		node   *corev1.Node
		reason string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "not found",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
		{
			name: "found",
			fields: func() fields {
				node := &types.V0044Node{V0044Node: v0044.V0044Node{Name: ptr.To("node-0")}}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			if err := r.MakeNodeDrain(tt.args.ctx, tt.args.node, tt.args.reason); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.MakeNodeDrain() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_MakeNodeUndrain(t *testing.T) {
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx    context.Context
		node   *corev1.Node
		reason string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "not found",
			fields: fields{
				Client: fake.NewFakeClient(),
			},
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
		{
			name: "found",
			fields: func() fields {
				node := &types.V0044Node{V0044Node: v0044.V0044Node{Name: ptr.To("node-0")}}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			if err := r.MakeNodeUndrain(tt.args.ctx, tt.args.node, tt.args.reason); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.MakeNodeUndrain() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_realSlurmControl_IsNodeDrain(t *testing.T) {
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx  context.Context
		node *corev1.Node
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "not found",
			fields: func() fields {
				return fields{
					Client: fake.NewFakeClient(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "not drain",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: v0044.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]v0044.V0044NodeState{v0044.V0044NodeStateIDLE}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "is drain",
			fields: func() fields {
				node := &types.V0044Node{
					V0044Node: v0044.V0044Node{
						Name:  ptr.To("node-0"),
						State: ptr.To([]v0044.V0044NodeState{v0044.V0044NodeStateIDLE, v0044.V0044NodeStateDRAIN}),
					},
				}
				return fields{
					Client: fake.NewClientBuilder().WithObjects(node).Build(),
				}
			}(),
			args: args{
				ctx:  context.TODO(),
				node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-0"}},
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				Client: tt.fields.Client,
			}
			got, err := r.IsNodeDrain(tt.args.ctx, tt.args.node)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.IsNodeDrain() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("realSlurmControl.IsNodeDrain() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_tolerateError(t *testing.T) {
	type args struct {
		err error
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Nil",
			args: args{
				err: nil,
			},
			want: true,
		},
		{
			name: "Empty",
			args: args{
				err: errors.New(""),
			},
			want: false,
		},
		{
			name: "NotFound",
			args: args{
				err: errors.New(http.StatusText(http.StatusNotFound)),
			},
			want: true,
		},
		{
			name: "NoContent",
			args: args{
				err: errors.New(http.StatusText(http.StatusNoContent)),
			},
			want: true,
		},
		{
			name: "Forbidden",
			args: args{
				err: errors.New(http.StatusText(http.StatusForbidden)),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tolerateError(tt.args.err); got != tt.want {
				t.Errorf("tolerateError() = %v, want %v", got, tt.want)
			}
		})
	}
}
