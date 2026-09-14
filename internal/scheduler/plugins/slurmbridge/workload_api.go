// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/SlinkyProject/slurm-bridge/internal/features"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

func newWorkloadAPI(config *rest.Config, scheme *runtime.Scheme) (*slurmjobir.WorkloadAPI, error) {
	if !utilfeature.DefaultFeatureGate.Enabled(features.SlurmBridgeGenericWorkload) {
		return nil, nil //nolint:nilnil // An explicitly disabled gate needs no discovery or API registration.
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	api, err := slurmjobir.RegisterWorkloadAPI(discoveryClient, scheme)
	if err != nil {
		return nil, fmt.Errorf("%s=true requires a supported Workload and PodGroup API: %w", features.SlurmBridgeGenericWorkload, err)
	}
	return api, nil
}
