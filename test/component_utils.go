// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/third_party/helm"
)

func CertMgrInstall(config *envconf.Config) error {
	manager := helm.New(config.KubeconfigFile())
	if err := manager.RunRepo(helm.WithArgs("add", "jetstack", "https://charts.jetstack.io")); err != nil {
		return err
	}
	if err := manager.RunRepo(helm.WithArgs("update")); err != nil {
		return err
	}
	return manager.RunInstall(helm.WithName("cert-manager"), helm.WithNamespace("cert-manager"),
		helm.WithReleaseName("jetstack/cert-manager"),
		helm.WithArgs("--set crds.enabled=true"),
		helm.WithWait(),
		helm.WithTimeout("5m"),
	)
}

func DoCertMgrInstall(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	if err := CertMgrInstall(config); err != nil {
		t.Fatal("failed to install cert-manager:", err)
	}

	return ctx
}

func SlurmOperatorCRDInstall(config *envconf.Config) error {
	manager := helm.New(config.KubeconfigFile())
	err := manager.RunInstall(
		helm.WithName("slurm-operator-crds"),
		helm.WithNamespace(SlinkyNamespace),
		helm.WithReleaseName("oci://ghcr.io/slinkyproject/charts/slurm-operator-crds"),
		helm.WithWait(),
		helm.WithTimeout("10m"))
	return err
}

func DoSlurmOperatorCRDInstall(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	if err := SlurmOperatorCRDInstall(config); err != nil {
		t.Fatal("failed to install slurm-operator crds:", err)
	}
	return ctx
}

func SlurmOperatorInstall(config *envconf.Config) error {
	manager := helm.New(config.KubeconfigFile())
	err := manager.RunInstall(
		helm.WithName("slurm-operator"),
		helm.WithNamespace(SlinkyNamespace),
		helm.WithReleaseName("oci://ghcr.io/slinkyproject/charts/slurm-operator"),
		helm.WithWait(),
		helm.WithTimeout("10m"),
		helm.WithArgs("--namespace=slinky", "--create-namespace"),
	)
	return err
}

func DoSlurmOperatorInstall(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	if err := SlurmOperatorInstall(config); err != nil {
		t.Fatal("failed to install slurm-operator:", err)
	}
	return ctx
}

func SlurmOperatorUninstall(config *envconf.Config) error {
	manager := helm.New(config.KubeconfigFile())
	return manager.RunUninstall(helm.WithName("slurm-operator"), helm.WithNamespace(SlinkyNamespace),
		helm.WithWait(), helm.WithTimeout("5m"))
}

func SlurmOperatorWebhookWaitReady(config *envconf.Config) error {
	webhookDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slurm-operator-webhook",
			Namespace: SlinkyNamespace,
		},
	}
	return wait.For(conditions.New(config.Client().Resources()).ResourceScaled(webhookDeployment, func(object k8s.Object) int32 {
		return object.(*appsv1.Deployment).Status.ReadyReplicas
	}, 1))
}

func SlurmChartInstall(config *envconf.Config) error {
	manager := helm.New(config.KubeconfigFile())
	return manager.RunInstall(
		helm.WithName("slurm"),
		helm.WithNamespace(SlurmNamespace),
		helm.WithReleaseName("oci://ghcr.io/slinkyproject/charts/slurm"),
		helm.WithArgs("--set", "partitions.slurm-bridge.enabled=true"),
		helm.WithArgs("--set", "partitions.slurm-bridge.nodesets={ALL}"),
		helm.WithArgs("--set", "controller.persistence.enabled=false"),
		helm.WithArgs("--namespace=slurm", "--create-namespace"),
		helm.WithWait(),
		helm.WithTimeout("15m"),
	)
}

func DoSlurmInstall(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	if err := SlurmChartInstall(config); err != nil {
		t.Fatal("failed to install slurm:", err)
	}
	return ctx
}

func SlurmOperatorCRDsUninstall(config *envconf.Config) error {
	manager := helm.New(config.KubeconfigFile())
	return manager.RunUninstall(helm.WithName("slurm-operator-crds"), helm.WithNamespace(SlinkyNamespace),
		helm.WithWait(), helm.WithTimeout("5m"))
}

// Source: https://slinky.schedmd.com/projects/slurm-bridge/en/main/quickstart.html#create-a-secret-for-slurm-bridge
func CreateSlurmBridgeToken(ctx context.Context, c client.Client) error {
	token := &slinkyv1beta1.Token{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slurm-bridge-token",
			Namespace: SlinkyNamespace,
		},
		Spec: slinkyv1beta1.TokenSpec{
			JwtHs256KeyRef: slinkyv1beta1.JwtSecretKeySelector{
				SecretKeySelector: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "slurm-auth-jwt"},
					Key:                  "jwt.key",
				},
				Namespace: SlurmNamespace,
			},
			SecretRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "slurm-bridge-token"},
				Key:                  "auth-token",
			},
			Username: "slurm",
			Refresh:  true,
		},
	}
	err := c.Create(ctx, token)
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func WaitForSlurmBridgeToken(ctx context.Context, c client.Client) error {
	return wait.For(func(ctx context.Context) (bool, error) {
		secret := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{
			Name:      "slurm-bridge-token",
			Namespace: SlinkyNamespace,
		}, secret); err != nil {
			return false, err
		}
		return len(secret.Data["auth-token"]) > 0, nil
	}, wait.WithTimeout(2*time.Minute), wait.WithInterval(5*time.Second))
}

func DoSlurmBridgeInstall(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	manager := helm.New(config.KubeconfigFile())

	crclient, err := GetControllerRuntimeClient(config)
	if err != nil {
		t.Fatalf("failed to get controller-runtime client: %v", err)
	}

	webhookDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slurm-operator-webhook",
			Namespace: SlinkyNamespace,
		},
	}
	if err := wait.For(conditions.New(config.Client().Resources()).ResourceScaled(webhookDeployment, func(object k8s.Object) int32 {
		return object.(*appsv1.Deployment).Status.ReadyReplicas
	}, 1)); err != nil {
		t.Fatalf("timed out waiting for slurm-operator-webhook: %v", err)
	}

	// create Token CRD; operator reconciles it into a real JWT secret
	err = CreateSlurmBridgeToken(ctx, crclient)
	if err != nil {
		t.Fatalf("token creation failed: %v", err)
	}
	if err := WaitForSlurmBridgeToken(ctx, crclient); err != nil {
		t.Fatalf("timed out waiting for slurm-bridge-token secret: %v", err)
	}

	opts := []helm.Option{
		helm.WithName("slurm"),
		helm.WithNamespace(SlinkyNamespace),
		helm.WithChart(Basepath + "helm/slurm-bridge"),
		helm.WithArgs("--values", filepath.Join(Basepath, "helm/slurm-bridge/values.yaml")),
		helm.WithArgs("--set", "scheduler.image.repository=ghcr.io/slinkyproject/slurm-bridge,scheduler.image.tag=e2e"),          // override
		helm.WithArgs("--set", "admission.image.repository=ghcr.io/slinkyproject/slurm-admission,admission.image.tag=e2e"),       // override
		helm.WithArgs("--set", "controllers.image.repository=ghcr.io/slinkyproject/slurm-controllers,controllers.image.tag=e2e"), // override
		helm.WithWait(),
		helm.WithTimeout("10m"),
	}

	err = manager.RunInstall(opts...)
	if err != nil {
		t.Fatalf("helm install failed: %v", err)
	}

	return ctx
}

func CheckDeploymentStatus(ctx context.Context, t *testing.T, config *envconf.Config, deploymentName string, deploymentNamespace string) context.Context {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: deploymentNamespace,
		},
	}
	err := wait.For(conditions.New(config.Client().Resources()).ResourceScaled(deployment, func(object k8s.Object) int32 {
		return object.(*appsv1.Deployment).Status.ReadyReplicas
	}, 1))
	if err != nil {
		t.Fatalf("failed waiting for the %s deployment to reach a ready state", deploymentName)
	}

	return ctx
}

func LabelWorkerNodesAsExternal(ctx context.Context, config *envconf.Config) error {
	crClient, err := GetControllerRuntimeClient(config)
	if err != nil {
		return fmt.Errorf("get client: %w", err)
	}
	nodeList := &corev1.NodeList{}
	if err := crClient.List(ctx, nodeList, client.MatchingLabels{
		"scheduler.slinky.slurm.net/slurm-bridge": "worker",
	}); err != nil {
		return fmt.Errorf("list worker nodes: %w", err)
	}
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		patch := client.MergeFrom(node.DeepCopy())
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels["scheduler.slinky.slurm.net/external-node"] = "true"
		if err := crClient.Patch(ctx, node, patch); err != nil {
			return fmt.Errorf("label node %s: %w", node.Name, err)
		}
	}
	return nil
}

// Note this will only annotate nodes that have the slurm-bridge worker label.
func AnnotateExternalNodePartitions(ctx context.Context, config *envconf.Config) error {
	cmd := exec.Command("kubectl", //nolint:gosec //WARN
		"--kubeconfig", config.KubeconfigFile(),
		"annotate", "nodes",
		"-l", "scheduler.slinky.slurm.net/external-node=true",
		"scheduler.slinky.slurm.net/external-node-partitions=slurm-bridge",
		"--overwrite",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("annotate external nodes: %w\n%s", err, out)
	}

	crClient, err := GetControllerRuntimeClient(config)
	if err != nil {
		return fmt.Errorf("get client: %w", err)
	}
	nodeList := &corev1.NodeList{}
	if err := crClient.List(ctx, nodeList, client.MatchingLabels{
		"scheduler.slinky.slurm.net/external-node": "true",
	}); err != nil {
		return fmt.Errorf("list external nodes: %w", err)
	}
	for _, node := range nodeList.Items {
		if node.Annotations["scheduler.slinky.slurm.net/external-node-partitions"] == "" {
			return fmt.Errorf("node %s missing external-node-partitions annotation", node.Name)
		}
	}
	return nil
}

func DoUninstallHelmChart(ctx context.Context, t *testing.T, config *envconf.Config, chartName string, chartNamespace string) context.Context {
	manager := helm.New(config.KubeconfigFile())

	err := manager.RunUninstall(
		helm.WithName(chartName),
		helm.WithNamespace(chartNamespace),
		helm.WithWait(),
		helm.WithTimeout("5m"),
	)

	if err != nil {
		t.Fatalf("failed to invoke helm uninstall %s due to an error: %v", chartName, err)
	}

	return ctx
}
