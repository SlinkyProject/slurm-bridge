// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
)

const (
	ConfigFile         = "/etc/slurm-bridge/config.yaml"
	SlurmClientTimeout = 5 * time.Minute
)

type Config struct {
	SchedulerName            string                `json:"schedulerName" yaml:"schedulerName"`
	SlurmRestApi             string                `json:"slurmRestApi" yaml:"slurmRestApi"`
	ManagedNamespaces        []string              `json:"managedNamespaces" yaml:"managedNamespaces"`
	ManagedNamespaceSelector *metav1.LabelSelector `json:"managedNamespaceSelector" yaml:"managedNamespaceSelector"`
	MCSLabel                 string                `json:"mcsLabel" yaml:"mcsLabel"`
	Partition                string                `json:"partition" yaml:"partition"`
	DeviceProfiles           []DeviceProfileConfig `json:"deviceProfiles" yaml:"deviceProfiles"`
}

// DeviceProfileConfig is the user-facing YAML representation of a DRA device
// profile.
type DeviceProfileConfig struct {
	Name     string                     `json:"name" yaml:"name"`
	Driver   string                     `json:"driver" yaml:"driver"`
	Selector string                     `json:"selector" yaml:"selector"`
	Backend  DeviceProfileBackendConfig `json:"backend" yaml:"backend"`
}

// DeviceProfileBackendConfig selects how Slurm represents a device profile.
type DeviceProfileBackendConfig struct {
	Type     string `json:"type" yaml:"type"`
	GRESName string `json:"gresName,omitempty" yaml:"gresName,omitempty"`
}

func (c *Config) ValidateScheduler() error {
	if strings.TrimSpace(c.MCSLabel) == "" {
		return errors.New("scheduler config mcsLabel must not be empty")
	}
	return nil
}

func Unmarshal(in []byte) (*Config, error) {
	out := &Config{}
	if err := yaml.UnmarshalStrict(in, out); err != nil {
		return nil, fmt.Errorf("parse slurm-bridge config: %w", err)
	}
	return out, nil
}

// DRARegistry converts the user-facing profile configuration into the runtime
// registry shared by all slurm-bridge components.
func (c *Config) DRARegistry() (*dra.Registry, error) {
	if c.DeviceProfiles == nil {
		return dra.DefaultRegistry(), nil
	}

	profiles := make([]dra.DeviceProfile, 0, len(c.DeviceProfiles))
	for _, configured := range c.DeviceProfiles {
		var backend dra.Backend
		switch configured.Backend.Type {
		case "core-bitmap":
			backend = dra.CoreBitmapBackend{}
		case "indexed-gres":
			backend = dra.IndexedGRESBackend{GRESName: configured.Backend.GRESName}
		default:
			return nil, fmt.Errorf("device profile %q has unsupported backend type %q", configured.Name, configured.Backend.Type)
		}
		profiles = append(profiles, dra.DeviceProfile{
			Name:     configured.Name,
			Driver:   configured.Driver,
			Selector: configured.Selector,
			Backend:  backend,
		})
	}
	return dra.NewRegistry(profiles)
}
