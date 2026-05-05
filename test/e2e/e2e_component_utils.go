// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/test"
	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// Slinky Component Health Checks

func checkControllerHealth(crClient crclient.Client, ctx context.Context, t *testing.T, config *envconf.Config) {
	// Get Controller CR
	controller := &slinkyv1beta1.Controller{}

	controllerKey := crclient.ObjectKey{
		Namespace: test.SlurmNamespace,
		Name:      "slurm",
	}

	err := crClient.Get(ctx, controllerKey, controller)
	if err != nil {
		t.Fatal("failed to Get() controller using controller-runtime client")
	}

	controllerUID := controller.UID

	// Get Controller StatefulSet using controller CR
	statefulSetKey := controller.Key()
	statefulSet := &appsv1.StatefulSet{}
	err = crClient.Get(ctx, statefulSetKey, statefulSet)
	if err != nil {
		t.Fatal("failed to Get() statefulset using controller-runtime client")
	}

	// Confirm ownership of controller statefulset
	for _, owner := range statefulSet.OwnerReferences {
		if owner.UID != controllerUID {
			t.Fatalf("dubious ownership of statefulset: %v", statefulSet)
		}
	}

	// Wait for controller statefulset to become ready
	err = wait.For(conditions.New(config.Client().Resources()).ResourceScaled(statefulSet, func(object k8s.Object) int32 {
		return object.(*appsv1.StatefulSet).Status.ReadyReplicas
	}, *statefulSet.Spec.Replicas))
	if err != nil {
		t.Fatalf("timed out waiting for StatefulSet %v to reach a ready state", statefulSet.Name)
	}
}

func checkRestAPIHealth(crClient crclient.Client, ctx context.Context, t *testing.T, config *envconf.Config) {
	// Get RestAPI CR
	restapi := &slinkyv1beta1.RestApi{}

	restapiKey := crclient.ObjectKey{
		Namespace: test.SlurmNamespace,
		Name:      "slurm",
	}

	err := crClient.Get(ctx, restapiKey, restapi)
	if err != nil {
		t.Fatal("failed to Get() restapi using controller-runtime client")
	}

	restapiUID := restapi.UID

	// Get RestAPI Deployment using RestAPI CR
	deploymentKey := restapi.Key()
	deployment := &appsv1.Deployment{}
	err = crClient.Get(ctx, deploymentKey, deployment)
	if err != nil {
		t.Fatal("failed to Get() deployment using controller-runtime client")
	}

	// Confirm ownership of RestAPI deployment
	for _, owner := range deployment.OwnerReferences {
		if owner.UID != restapiUID {
			t.Fatalf("dubious ownership of deployment: %v", deployment)
		}
	}

	// Check whether RestAPI deployment is healthy
	err = wait.For(conditions.New(config.Client().Resources()).ResourceScaled(deployment, func(object k8s.Object) int32 {
		return object.(*appsv1.Deployment).Status.ReadyReplicas
	}, *deployment.Spec.Replicas))
	if err != nil {
		t.Fatalf("timed out waiting for Deployment %v to reach a ready state", deployment.Name)
	}
}
