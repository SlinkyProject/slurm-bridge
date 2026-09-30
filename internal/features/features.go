// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package features

import (
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	kubefeatures "k8s.io/kubernetes/pkg/features"
)

// SlurmBridgeGenericWorkload enables bridge support for built-in Workload and
// PodGroup APIs. It does not enable the embedded scheduler's GenericWorkload gate.
const SlurmBridgeGenericWorkload featuregate.Feature = "SlurmBridgeGenericWorkload"

func init() {
	// Kubernetes 1.37 enables this informer by default, but its v1 API is not
	// served by older supported clusters. Preserve the 1.36 scheduler default.
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.OverrideDefault(kubefeatures.DRADeviceTaintRules, false))
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		SlurmBridgeGenericWorkload: {Default: true, PreRelease: featuregate.Beta},
	}))
}
