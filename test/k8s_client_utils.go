// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package test

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
)

func GetControllerRuntimeClient(config *envconf.Config) (crclient.Client, error) {
	var scheme = k8sruntime.NewScheme()
	err := appsv1.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}
	err = corev1.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}
	err = batchv1.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}
	err = slinkyv1beta1.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}

	return klient.NewControllerRuntimeClient(config.Client().RESTConfig(), scheme)
}
