// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package externaljobinfo

import (
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/ptr"
)

func TestExternalJobInfo_Equal(t *testing.T) {
	type fields struct {
		Pods []string
	}
	type args struct {
		cmp ExternalJobInfo
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   bool
	}{
		{
			name: "Empty",
			fields: fields{
				Pods: []string{},
			},
			args: args{
				cmp: ExternalJobInfo{
					Pods: []string{},
				},
			},
			want: true,
		},
		{
			name: "Equal",
			fields: fields{
				Pods: []string{"bar/foo"},
			},
			args: args{
				cmp: ExternalJobInfo{
					Pods: []string{"bar/foo"},
				},
			},
			want: true,
		},
		{
			name: "Not equal",
			fields: fields{
				Pods: []string{"bar/foo"},
			},
			args: args{
				cmp: ExternalJobInfo{
					Pods: []string{"buz/biz"},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extInfo := &ExternalJobInfo{
				Pods: tt.fields.Pods,
			}
			if got := extInfo.Equal(tt.args.cmp); got != tt.want {
				t.Errorf("ExternalJobInfo.Equal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExternalJobInfo_ToString(t *testing.T) {
	type fields struct {
		Pods []string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "Empty",
			fields: fields{
				Pods: []string{},
			},
			want: `{"pods":[]}`,
		},
		{
			name: "With a pod",
			fields: fields{
				Pods: []string{"bar/foo"},
			},
			want: `{"pods":["bar/foo"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podInfo := &ExternalJobInfo{
				Pods: tt.fields.Pods,
			}
			if got := podInfo.ToString(); got != tt.want {
				t.Errorf("ExternalJobInfo.ToString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseIntoExternalJobInfo(t *testing.T) {
	type args struct {
		str *string
		out *ExternalJobInfo
	}
	tests := []struct {
		name    string
		args    args
		want    *ExternalJobInfo
		wantErr bool
	}{
		{
			name: "Empty string",
			args: args{
				str: ptr.To(""),
				out: &ExternalJobInfo{},
			},
			want:    &ExternalJobInfo{},
			wantErr: true,
		},
		{
			name: "Empty values",
			args: args{
				str: ptr.To(`{"pods":[]}`),
				out: &ExternalJobInfo{},
			},
			want:    &ExternalJobInfo{Pods: []string{}},
			wantErr: false,
		},
		{
			name: "Single pod",
			args: args{
				str: ptr.To(`{"pods":["bar/foo"]}`),
				out: &ExternalJobInfo{},
			},
			want:    &ExternalJobInfo{Pods: []string{"bar/foo"}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ParseIntoExternalJobInfo(tt.args.str, tt.args.out); (err != nil) != tt.wantErr {
				t.Errorf("ParseIntoExternalJobInfo() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got := tt.args.out; !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("ParseIntoExternalJobInfo() = %v, want %v", got, tt.want)
			}
		})
	}
}
