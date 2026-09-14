// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package features

import (
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
)

// SlurmBridgeGenericWorkload enables bridge support for built-in Workload and
// PodGroup APIs. It does not enable the embedded scheduler's GenericWorkload gate.
const SlurmBridgeGenericWorkload featuregate.Feature = "SlurmBridgeGenericWorkload"

func init() {
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		SlurmBridgeGenericWorkload: {Default: true, PreRelease: featuregate.Beta},
	}))
}
