// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/testutils"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

const schedulerServiceAccount = "slurm-bridge-scheduler"

var (
	cfg             *rest.Config
	k8sClient       client.Client
	schedulerClient client.Client
	testEnv         *envtest.Environment
	ctx             context.Context
	cancel          context.CancelFunc
)

func TestSlurmBridgeIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SlurmBridge Integration Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	repoRoot := filepath.Join("..", "..", "..", "..")

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		ErrorIfCRDPathMissing: false,
		BinaryAssetsDirectory: testutils.GetEnvTestBinary(repoRoot),
	}
	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("applying generated scheduler ClusterRole")
	roleBytes, err := os.ReadFile(filepath.Join(repoRoot, "config", "rbac", "scheduler", "role.yaml"))
	Expect(err).NotTo(HaveOccurred(), "run `make manifests` to generate config/rbac/scheduler/role.yaml")
	role := &rbacv1.ClusterRole{}
	Expect(yaml.Unmarshal(roleBytes, role)).To(Succeed())
	role.ResourceVersion = ""
	Expect(k8sClient.Create(ctx, role)).To(Succeed())

	By("creating scheduler ServiceAccount and ClusterRoleBinding")
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: schedulerServiceAccount, Namespace: corev1.NamespaceDefault},
	}
	Expect(k8sClient.Create(ctx, sa)).To(Succeed())
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "slurm-bridge-scheduler-test"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{{
			Kind: rbacv1.ServiceAccountKind, Name: schedulerServiceAccount, Namespace: corev1.NamespaceDefault,
		}},
	}
	Expect(k8sClient.Create(ctx, crb)).To(Succeed())

	By("building impersonated client for scheduler ServiceAccount")
	impCfg := rest.CopyConfig(cfg)
	impCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + corev1.NamespaceDefault + ":" + schedulerServiceAccount,
	}
	schedulerClient, err = client.New(impCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(schedulerClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})
