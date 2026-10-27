// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package features

import (
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	kubefeatures "k8s.io/kubernetes/pkg/features"
)

// GenericWorkload controls built-in Workload support and native group scheduling
// through the same upstream feature gate in every bridge component.
const GenericWorkload = kubefeatures.GenericWorkload

// Legacyv1alpha1PodGroups allows deprecated scheduler-plugins PodGroups
// through admission with a warning. Disabling it rejects new usage while
// preserving updates to Pods already using the feature.
const Legacyv1alpha1PodGroups featuregate.Feature = "Legacyv1alpha1PodGroups"

// LegacyLWSGangs allows implicit LeaderWorkerSet gangs through admission
// with a warning. Disabling it rejects new usage while preserving updates to
// Pods already using the feature.
const LegacyLWSGangs featuregate.Feature = "LegacyLWSGangs"

func init() {
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.OverrideDefault(GenericWorkload, true))
	// Kubernetes 1.37 enables this informer by default, but its v1 API is not
	// served by older supported clusters. Preserve the 1.36 scheduler default.
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.OverrideDefault(kubefeatures.DRADeviceTaintRules, false))
	utilruntime.Must(utilfeature.DefaultMutableFeatureGate.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		Legacyv1alpha1PodGroups: {Default: true, PreRelease: featuregate.Deprecated},
		LegacyLWSGangs:          {Default: true, PreRelease: featuregate.Deprecated},
	}))
}
