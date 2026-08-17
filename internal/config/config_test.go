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
			name: "Test managedNamespaceSelector",
			args: args{
				in: []byte(`
managedNamespaceSelector:
  matchLabels:
    slurm-bridge: managed
`),
			},
			want: &Config{
				ManagedNamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"slurm-bridge": "managed"},
				},
			},
			wantErr: false,
		},
		{
			name: "Test deviceProfiles",
			args: args{in: []byte(`
deviceProfiles:
  - name: custom-accelerator
    driver: accelerator.example.com
    selector: device.driver == 'accelerator.example.com'
    backend:
      type: indexed-gres
      gresName: accelerator
`)},
			want: &Config{DeviceProfiles: []DeviceProfileConfig{{
				Name:     "custom-accelerator",
				Driver:   "accelerator.example.com",
				Selector: `device.driver == 'accelerator.example.com'`,
				Backend: DeviceProfileBackendConfig{
					Type:     "indexed-gres",
					GRESName: "accelerator",
				},
			}}},
			wantErr: false,
		},
		{
			name: "Reject unknown field",
			args: args{in: []byte(`
deviceProfiles:
  - name: custom-accelerator
    driver: accelerator.example.com
    selector: device.driver == 'accelerator.example.com'
    backend:
      type: indexed-gres
      gresNam: accelerator
`)},
			wantErr: true,
		},
		{
			name: "Reject duplicate field",
			args: args{in: []byte(`
schedulerName: first
schedulerName: second
`)},
			wantErr: true,
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

func TestConfigDRARegistry(t *testing.T) {
	cfg := &Config{DeviceProfiles: []DeviceProfileConfig{{
		Name:     "custom-accelerator",
		Driver:   "accelerator.example.com",
		Selector: `device.driver == 'accelerator.example.com'`,
		Backend: DeviceProfileBackendConfig{
			Type:     "indexed-gres",
			GRESName: "accelerator",
		},
	}}}

	registry, err := cfg.DRARegistry()
	if err != nil {
		t.Fatalf("Config.DRARegistry() error = %v", err)
	}
	profile, ok := registry.LookupByName("custom-accelerator")
	if !ok {
		t.Fatal("Config.DRARegistry() omitted configured profile")
	}
	gres, err := profile.GRES()
	if err != nil {
		t.Fatalf("DeviceProfile.GRES() error = %v", err)
	}
	if gres.Name != "accelerator" || gres.Type != "custom-accelerator" {
		t.Fatalf("DeviceProfile.GRES() = %#v", gres)
	}
}

func TestConfigDRARegistryRejectsInvalidBackend(t *testing.T) {
	cfg := &Config{DeviceProfiles: []DeviceProfileConfig{{
		Name:    "broken",
		Backend: DeviceProfileBackendConfig{Type: "unknown"},
	}}}
	if _, err := cfg.DRARegistry(); err == nil {
		t.Fatal("Config.DRARegistry() error = nil, want unsupported backend error")
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

func TestConfig_ValidateScheduler(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name:   "valid MCS label",
			config: Config{MCSLabel: "kubernetes"},
		},
		{
			name:    "empty MCS label",
			config:  Config{},
			wantErr: true,
		},
		{
			name:    "whitespace MCS label",
			config:  Config{MCSLabel: "  "},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.config.ValidateScheduler(); (err != nil) != tt.wantErr {
				t.Errorf("Config.ValidateScheduler() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
