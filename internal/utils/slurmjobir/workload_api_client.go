// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
)

// NewWorkloadAPI configures native workload support for a controller or scheduler.
// When enabled, discovery or API availability failures must prevent startup.
func NewWorkloadAPI(config *rest.Config, scheme *runtime.Scheme) (*WorkloadAPI, error) {
	if !utilfeature.DefaultFeatureGate.Enabled(features.SlurmBridgeGenericWorkload) {
		return nil, nil //nolint:nilnil // An explicitly disabled gate needs no discovery or API registration.
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	api, err := RegisterWorkloadAPI(discoveryClient, scheme)
	if err != nil {
		return nil, fmt.Errorf("%s=true requires a supported Workload and PodGroup API: %w", features.SlurmBridgeGenericWorkload, err)
	}
	return api, nil
}
