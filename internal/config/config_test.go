// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUnmarshal(t *testing.T) {
	type args struct {
		in []byte
	}
	tests := []struct {
		name    string
		args    args
		want    *Config
		wantErr bool
	}{
		{
			name: "Empty",
			args: args{
				in: []byte{},
			},
			want: &Config{},
		},
		{
			name: "Test schedulerName",
			args: args{
				in: []byte(`schedulerName: slurm-bridge-scheduler`),
			},
			want: &Config{
				SchedulerName: "slurm-bridge-scheduler",
			},
			wantErr: false,
		},
		{
			name: "Test slurmRestApi",
			args: args{
				in: []byte(`slurmRestApi: test1`),
			},
			want: &Config{
				SlurmRestApi: "test1",
			},
			wantErr: false,
		},
		{
			name: "Test managedNamespaces",
			args: args{
				in: []byte(`managedNamespaces:
- Item1
- Item2
`),
			},
			want: &Config{
				ManagedNamespaces: []string{"Item1", "Item2"},
			},
			wantErr: false,
		},
		{
			name: "Test MCSLabel",
			args: args{
				in: []byte(`mcsLabel: kubernetes`),
			},
			want: &Config{
				MCSLabel: "kubernetes",
			},
			wantErr: false,
		},
		{
			name: "Test partition",
			args: args{
				in: []byte(`partition: slurm-bridge`),
			},
			want: &Config{
				Partition: "slurm-bridge",
			},
			wantErr: false,
		},
		{
			name: "Test managedNamespaceSelector with matchLabels",
			args: args{
				in: []byte(`managedNamespaceSelector:
  matchLabels:
    slurm-managed: true
`),
			},
			want: &Config{
				ManagedNamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"slurm-managed": "true",
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Unmarshal(tt.args.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnmarshalOrDie(t *testing.T) {
	type args struct {
		in []byte
	}
	tests := []struct {
		name string
		args args
		want *Config
	}{
		{
			name: "Empty",
			args: args{
				in: []byte{},
			},
			want: &Config{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnmarshalOrDie(tt.args.in); !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("UnmarshalOrDie() = %v, want %v", got, tt.want)
			}
		})
	}
}
