// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// TestUpgrade requires suite=upgrade: it upgrades the installed bridge release
// and must run alone on a dedicated baseline cluster.
func TestUpgrade(t *testing.T) {
	requireE2ESuite(t, "upgrade", true)
	fromVersion := os.Getenv("E2E_UPGRADE_FROM_VERSION")
	if _, err := utilversion.ParseSemantic(fromVersion); err != nil {
		t.Fatalf("E2E_UPGRADE_FROM_VERSION must name the installed chart version: %v", err)
	}
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	nodeMode, err := parseSlurmNodeModeFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseE2ECleanupFromEnvironment(); err != nil {
		t.Fatal(err)
	}

	for _, feature := range []types.Feature{
		testSlurmBridgeUpgrade(fromVersion),
		// The current readiness check requires features not present in 1.2.
		testSlurmBridgeReadiness(nodeMode),
		testSlurmBridgeJobScheduling(),
		testAdmissionRoutingBoundaries(),
	} {
		_ = testEnv.Test(t, withSuiteLabel(feature, "upgrade"))
		if t.Failed() {
			return
		}
	}
}

func testSlurmBridgeUpgrade(fromVersion string) types.Feature {
	chart := filepath.Join(repositoryRoot(), "helm", "slurm-bridge")
	running := slurmTestPod(slurmBridgeNamespace, envconf.RandomName("upgrade-running", 40),
		[]string{"sh", "-c", "sleep 3600"})
	pending := slurmTestPod(slurmBridgeNamespace, envconf.RandomName("upgrade-pending", 40),
		[]string{"sh", "-c", "sleep 3600"})
	occupants := []*corev1.Pod{running}
	var before, target upgradeMetadata
	var crClient client.Client
	var baselineImages map[string]string

	return features.New("Bridge upgrade preserves running and pending workloads").
		WithLabel("suite", "upgrade").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			// Cleanup must also run when baseline preparation fails.
			t.Cleanup(func() {
				captureReleaseSignalDiagnostics(t, "bridge upgrade", slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
				if crClient != nil && e2eCleanupEnabled(t) {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*slurmCleanupTimeout)
					defer cancel()
					deletePodsAndAssertCleanup(cleanupCtx, t, config, crClient, append(occupants, pending)...)
				}
			})
			var err error
			crClient, err = getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			before = getUpgradeMetadata(ctx, t)
			if before.Chart != "slurm-bridge" || before.Version != fromVersion || before.Status != "deployed" {
				t.Fatalf("expected deployed slurm-bridge %s, got %+v", fromVersion, before)
			}
			baselineImages = upgradeDeploymentImages(ctx, t, crClient)
			output := runUpgradeHelm(ctx, t, "show", "chart", chart)
			if err := yaml.Unmarshal(output, &target); err != nil {
				t.Fatalf("decode target chart: %v", err)
			}
			if target.Name != "slurm-bridge" {
				t.Fatalf("target must be a slurm-bridge chart, got %+v", target)
			}
			if err := validateUpgradeTestVersions(fromVersion, target.Version); err != nil {
				t.Fatal(err)
			}
			t.Logf("upgrading chart %s (app %s, revision %d) to %s (app %s)",
				before.Version, before.AppVersion, before.Revision, target.Version, target.AppVersion)

			nodes := &corev1.NodeList{}
			if err := crClient.List(ctx, nodes, client.MatchingLabels{slurmBridgeWorkerLabel: "worker"}); err != nil {
				t.Fatal(err)
			}
			if len(nodes.Items) == 0 {
				t.Fatal("no bridge worker available for the upgrade workloads")
			}
			// 1.2 does not constrain Slurm allocations by Kubernetes nodeSelector.
			// Occupy every bridge worker instead so pending work cannot run elsewhere.
			for range len(nodes.Items) - 1 {
				occupants = append(occupants, slurmTestPod(slurmBridgeNamespace,
					envconf.RandomName("upgrade-running", 40), []string{"sh", "-c", "sleep 3600"}))
			}
			occupied := make(map[string]bool, len(nodes.Items))
			for i, pod := range occupants {
				pod.Annotations = map[string]string{wellknown.AnnotationExclusive: "true"}
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create running workload: %v", err)
				}
				observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), upgradePodRunning)
				if err != nil {
					t.Fatalf("baseline workload did not run: %v", err)
				}
				assertBridgePod(t, ctx, crClient, observed)
				if occupied[observed.Spec.NodeName] {
					t.Fatalf("exclusive baseline workloads share node %s", observed.Spec.NodeName)
				}
				occupied[observed.Spec.NodeName] = true
				occupants[i] = observed
			}
			running = occupants[0]
			pending.Annotations = map[string]string{wellknown.AnnotationExclusive: "true"}
			if err := crClient.Create(ctx, pending); err != nil {
				t.Fatalf("create pending workload: %v", err)
			}
			pending, err = waitForPod(ctx, crClient, client.ObjectKeyFromObject(pending), func(pod *corev1.Pod) bool {
				if pod.Spec.NodeName != "" {
					t.Fatal("pending workload bound despite exclusive allocations occupying every bridge worker")
				}
				return pod.Status.Phase == corev1.PodPending && slices.ContainsFunc(pod.Status.Conditions,
					func(condition corev1.PodCondition) bool {
						return condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse
					})
			})
			if err != nil {
				t.Fatalf("baseline scheduler did not leave the extra workload pending: %v", err)
			}
			return ctx
		}).
		Assess("the normal deployment upgrades the existing release", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			t.Log(string(runUpgradeCommand(ctx, t, "make", "deploy")))
			after := getUpgradeMetadata(ctx, t)
			if after.Version != target.Version || after.AppVersion != target.AppVersion ||
				after.Revision != before.Revision+1 || after.Status != "deployed" {
				t.Fatalf("unexpected upgraded release: %+v", after)
			}
			for component, image := range upgradeDeploymentImages(ctx, t, crClient) {
				if image == baselineImages[component] {
					t.Fatalf("%s still uses the baseline image %s; target values must select the new binaries", component, image)
				}
				t.Logf("%s image: %s -> %s", component, baselineImages[component], image)
			}
			return ctx
		}).
		Assess("running allocation survives and pending work resumes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			observed := &corev1.Pod{}
			for _, pod := range occupants {
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), observed); err != nil {
					t.Fatal(err)
				}
				if err := upgradePodPreserved(pod, observed); err != nil {
					t.Fatalf("running workload %s: %v", pod.Name, err)
				}
				output, err := querySlurmJob(ctx, config, crClient, pod.Labels[slurmJobIDLabel])
				if err != nil {
					t.Fatal(err)
				}
				state, err := slurmJobField(output, "JobState")
				if err != nil || state != "RUNNING" {
					t.Fatalf("original allocation for %s is no longer RUNNING: state=%q, error=%v", pod.Name, state, err)
				}
			}
			if err := crClient.Get(ctx, client.ObjectKeyFromObject(pending), observed); err != nil {
				t.Fatal(err)
			}
			if observed.UID != pending.UID || observed.Spec.NodeName != "" || observed.Status.Phase != corev1.PodPending {
				t.Fatalf("pending workload was replaced or bound before capacity was released: %s", statusJSON(observed))
			}

			// Releasing capacity is part of the assertion, even with E2E_CLEANUP=false.
			deletePodAndAssertCleanup(ctx, t, config, crClient, running)
			if t.Failed() {
				return ctx
			}
			observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pending), upgradePodRunning)
			if err != nil {
				t.Fatalf("pre-upgrade pending workload did not run after capacity was released: %v", err)
			}
			if observed.UID != pending.UID || observed.Spec.NodeName != running.Spec.NodeName {
				t.Fatal("pending workload was replaced or did not use the released node")
			}
			if jobID := pending.Labels[slurmJobIDLabel]; jobID != "" && observed.Labels[slurmJobIDLabel] != jobID {
				t.Fatalf("pending allocation changed from %s to %s", jobID, observed.Labels[slurmJobIDLabel])
			}
			pending = observed
			assertBridgePod(t, ctx, crClient, pending)
			deletePodAndAssertCleanup(ctx, t, config, crClient, pending)
			return ctx
		}).Feature()
}

func validateUpgradeTestVersions(fromVersion, toVersion string) error {
	from, err := utilversion.ParseSemantic(fromVersion)
	if err != nil {
		return fmt.Errorf("parse baseline version: %w", err)
	}
	to, err := utilversion.ParseSemantic(toVersion)
	if err != nil {
		return fmt.Errorf("parse target version: %w", err)
	}
	if !from.LessThan(to) {
		return fmt.Errorf("target version %s must be newer than baseline %s", toVersion, fromVersion)
	}
	if from.Major() != to.Major() {
		return fmt.Errorf("this upgrade spec only covers releases with the same first version number: %s to %s", fromVersion, toVersion)
	}
	if to.Minor() > from.Minor()+1 {
		return fmt.Errorf("unsupported upgrade from %s to %s: skipping release series is not supported; upgrade one series at a time", fromVersion, toVersion)
	}
	return nil
}

type upgradeMetadata struct {
	Name       string `json:"name"`
	Chart      string `json:"chart"`
	Version    string `json:"version"`
	AppVersion string `json:"appVersion"`
	Revision   int    `json:"revision"`
	Status     string `json:"status"`
}

func getUpgradeMetadata(ctx context.Context, t *testing.T) upgradeMetadata {
	t.Helper()
	var metadata upgradeMetadata
	if err := json.Unmarshal(runUpgradeHelm(ctx, t, "get", "metadata", "slurm-bridge", "--output", "json"), &metadata); err != nil {
		t.Fatalf("decode Helm release metadata: %v", err)
	}
	return metadata
}

func runUpgradeHelm(ctx context.Context, t *testing.T, args ...string) []byte {
	t.Helper()
	args = append([]string{"--kube-context", os.Getenv(e2eKubeContextEnvironment), "--namespace", slurmNamespace}, args...)
	return runUpgradeCommand(ctx, t, "helm", args...)
}

func runUpgradeCommand(ctx context.Context, t *testing.T, name string, args ...string) []byte {
	t.Helper()
	kubeContext := os.Getenv(e2eKubeContextEnvironment)
	if kubeContext == "" {
		t.Fatalf("%s must name the upgrade cluster context", e2eKubeContextEnvironment)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // Test commands use explicit arguments, never a shell.
	command.Dir = repositoryRoot()
	// make deploy runs Skaffold; keep it on the same explicit context as the test.
	command.Env = append(command.Environ(), "SKAFFOLD_KUBE_CONTEXT="+kubeContext)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s\n%s", name, args, err, output, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Logf("%s: %s", name, stderr.String())
	}
	return output
}

func upgradeDeploymentImages(ctx context.Context, t *testing.T, crClient client.Client) map[string]string {
	t.Helper()
	images := map[string]string{}
	for _, component := range []string{"scheduler", "controllers", "admission"} {
		deployment := &appsv1.Deployment{}
		key := client.ObjectKey{Namespace: slurmNamespace, Name: "slurm-bridge-" + component}
		if err := wait.For(func(ctx context.Context) (bool, error) {
			if err := crClient.Get(ctx, key, deployment); err != nil {
				return false, err
			}
			replicas := deployment.Spec.Replicas
			return replicas != nil && *replicas > 0 && deployment.Status.ObservedGeneration >= deployment.Generation &&
				deployment.Status.UpdatedReplicas == *replicas && deployment.Status.AvailableReplicas == *replicas &&
				deployment.Status.Replicas == *replicas, nil
		}, wait.WithContext(ctx), wait.WithTimeout(slurmBridgeReadinessTimeout), wait.WithInterval(2*time.Second)); err != nil {
			t.Fatalf("deployment %s did not finish rolling out: %v", key, err)
		}
		images[component] = deployment.Spec.Template.Spec.Containers[0].Image
	}
	return images
}

func upgradePodRunning(pod *corev1.Pod) bool {
	return podHasSlurmAllocation(pod) && pod.Status.Phase == corev1.PodRunning &&
		len(pod.Status.ContainerStatuses) == 1 && pod.Status.ContainerStatuses[0].State.Running != nil &&
		pod.Status.ContainerStatuses[0].ContainerID != ""
}

func upgradePodPreserved(before, after *corev1.Pod) error {
	if !upgradePodRunning(after) || after.DeletionTimestamp != nil || before.UID != after.UID ||
		before.Spec.NodeName != after.Spec.NodeName || before.Labels[slurmJobIDLabel] != after.Labels[slurmJobIDLabel] ||
		before.Annotations[wellknown.AnnotationExternalJobNode] != after.Annotations[wellknown.AnnotationExternalJobNode] {
		return fmt.Errorf("running workload identity or allocation changed during upgrade: %s", statusJSON(after))
	}
	oldStatus, newStatus := before.Status.ContainerStatuses[0], after.Status.ContainerStatuses[0]
	if oldStatus.ContainerID != newStatus.ContainerID || oldStatus.RestartCount != newStatus.RestartCount {
		return fmt.Errorf("running container restarted during upgrade: before=%s, after=%s", statusJSON(oldStatus), statusJSON(newStatus))
	}
	return nil
}
