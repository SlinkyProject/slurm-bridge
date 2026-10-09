// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func testHybridMCSIsolation() types.Feature {
	return features.New("Hybrid MCS isolation").
		WithLabel(slurmNodeModeLabel, string(slurmNodeModeHybrid)).
		Assess("native and Kubernetes workloads remain isolated in both submission orders", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			controller, err := getSlurmControllerPod(ctx, crClient)
			if err != nil {
				t.Fatal(err)
			}
			pod := slurmTestPod(slurmBridgeNamespace, envconf.RandomName("mcs-isolation", 40),
				[]string{"sh", "-c", "while [ ! -e /tmp/finish ]; do sleep 1; done"})
			var nativeID, partition string
			t.Cleanup(func() {
				captureReleaseSignalDiagnostics(t, "Hybrid MCS isolation", slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
				if !e2eCleanupEnabled(t) {
					return
				}
				cleanupCtx, cancel := context.WithTimeout(context.Background(), slurmCleanupTimeout)
				defer cancel()
				deletePodAndAssertCleanup(cleanupCtx, t, config, crClient, pod)
				if nativeID != "" {
					if _, err := execInPod(cleanupCtx, config, controller, "scancel", nativeID); err != nil {
						t.Errorf("cancel native job: %v", err)
					}
				}
				if partition != "" {
					if _, err := execInPod(cleanupCtx, config, controller, "scontrol", "delete", "PartitionName="+partition); err != nil {
						t.Errorf("delete test partition: %v", err)
					}
				}
			})

			// Leave spare CPUs and memory so MCS, rather than resource exhaustion,
			// is what prevents the bridge job from sharing the native job's node.
			output, err := execInPod(ctx, config, controller, "sbatch", "--parsable",
				"--job-name="+pod.Name, "--partition="+slurmBridgePartition,
				"--nodes=1", "--ntasks=1", "--cpus-per-task=1", "--mem=100M",
				"--time=5", "--chdir=/tmp", "--output=/dev/null", "--wrap=sleep 300")
			if err != nil {
				t.Fatalf("submit native job: %v", err)
			}
			nativeID, _, _ = strings.Cut(strings.TrimSpace(output), ";")
			if nativeID == "" {
				t.Fatal("sbatch returned no job ID")
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				output, err = querySlurmJob(ctx, config, crClient, nativeID)
				if err != nil {
					return false, err
				}
				state, err := slurmJobField(output, "JobState")
				if err == nil && state != "PENDING" && state != "RUNNING" {
					return false, fmt.Errorf("native job reached %s: %s", state, output)
				}
				return state == "RUNNING", err
			}, wait.WithContext(ctx), wait.WithTimeout(slurmBridgeReadinessTimeout), wait.WithInterval(time.Second)); err != nil {
				t.Fatalf("native job did not start: %v", err)
			}
			node, err := slurmJobNodeList(output)
			if err != nil {
				t.Fatal(err)
			}
			// Constrain the Slurm allocation itself: the bridge scheduler does not
			// honor Kubernetes node selectors. Otherwise, on a multi-node cluster,
			// Slurm can start the bridge job on another idle node.
			if _, err := execInPod(ctx, config, controller, "scontrol", "create",
				"PartitionName="+pod.Name, "Nodes="+node, "Default=NO", "State=UP"); err != nil {
				t.Fatalf("create single-node test partition: %v", err)
			}
			partition = pod.Name
			pod.Annotations = map[string]string{
				wellknown.AnnotationExclusive: "false",
				wellknown.AnnotationJobName:   pod.Name,
				wellknown.AnnotationPartition: partition,
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create non-exclusive pod: %v", err)
			}
			pod, err = waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), func(p *corev1.Pod) bool {
				return p.Labels[slurmJobIDLabel] != ""
			})
			if err != nil {
				t.Fatalf("bridge did not submit a job: %v", err)
			}
			bridgeID := pod.Labels[slurmJobIDLabel]

			// Observe a changed job name in Slurm to prove the bridge has updated
			// this pending job; merely observing the initial submission is insufficient.
			before := pod.DeepCopy()
			updatedName := pod.Name + "-updated"
			pod.Annotations[wellknown.AnnotationJobName] = updatedName
			if err := crClient.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
				t.Fatalf("change pending job name: %v", err)
			}
			var updatedAt time.Time
			if err := wait.For(func(ctx context.Context) (bool, error) {
				native, err := querySlurmJob(ctx, config, crClient, nativeID)
				if err != nil {
					return false, err
				}
				if state, _ := slurmJobField(native, "JobState"); state != "RUNNING" {
					return false, fmt.Errorf("native blocker stopped before the isolation check: %s", native)
				}
				bridge, err := querySlurmJob(ctx, config, crClient, bridgeID)
				if err != nil {
					return false, err
				}
				// Slurm 25.11 reports MCS in OverSubscribe; 26.05 uses Exclusive.
				sharing, err := slurmJobField(bridge, "Exclusive")
				if err != nil {
					sharing, err = slurmJobField(bridge, "OverSubscribe")
				}
				if err != nil {
					return false, err
				}
				if state, _ := slurmJobField(bridge, "JobState"); state != "PENDING" || sharing != "MCS" {
					return false, fmt.Errorf("bridge job did not remain pending with MCS while native job %s was running: %s", nativeID, bridge)
				}
				if name, _ := slurmJobField(bridge, "JobName"); name == updatedName && updatedAt.IsZero() {
					updatedAt = time.Now()
					t.Logf("bridge job %s was updated and remains pending with MCS on native job %s's node %s", bridgeID, nativeID, node)
				}
				return !updatedAt.IsZero() && time.Since(updatedAt) >= 10*time.Second, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmBridgeReadinessTimeout), wait.WithInterval(time.Second)); err != nil {
				t.Fatal(err)
			}

			if _, err := execInPod(ctx, config, controller, "scancel", nativeID); err != nil {
				t.Fatalf("release native allocation: %v", err)
			}
			pod, err = waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), func(p *corev1.Pod) bool {
				return p.Status.Phase == corev1.PodRunning && podHasSlurmAllocation(p)
			})
			if err != nil {
				t.Fatalf("pod did not start after native work ended: %v", err)
			}
			if pod.Spec.NodeName != node || pod.Labels[slurmJobIDLabel] != bridgeID {
				t.Fatalf("expected the same pending job to start on %s, got node=%s job=%s", node, pod.Spec.NodeName, pod.Labels[slurmJobIDLabel])
			}
			t.Logf("pod %s started on %s after native work ended", pod.Name, node)

			// Reverse the order: native work must wait for the running Kubernetes
			// workload. Pin it to the same node so it cannot use another idle node.
			output, err = execInPod(ctx, config, controller, "sbatch", "--parsable",
				"--job-name="+pod.Name+"-native", "--partition="+slurmBridgePartition,
				"--nodelist="+node, "--nodes=1", "--ntasks=1", "--cpus-per-task=1", "--mem=100M",
				"--time=5", "--chdir=/tmp", "--output=/dev/null", "--wrap=sleep 300")
			if err != nil {
				t.Fatalf("submit native job after Kubernetes: %v", err)
			}
			nativeID, _, _ = strings.Cut(strings.TrimSpace(output), ";")
			if nativeID == "" {
				t.Fatal("sbatch returned no job ID")
			}
			pendingSince := time.Now()
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
					return false, err
				}
				if pod.Status.Phase != corev1.PodRunning {
					return false, fmt.Errorf("Kubernetes blocker stopped before the isolation check: %s", pod.Status.Phase)
				}
				native, err := querySlurmJob(ctx, config, crClient, nativeID)
				if err != nil {
					return false, err
				}
				if state, _ := slurmJobField(native, "JobState"); state != "PENDING" {
					return false, fmt.Errorf("native job did not wait for Kubernetes pod %s: %s", pod.Name, native)
				}
				return time.Since(pendingSince) >= 10*time.Second, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmBridgeReadinessTimeout), wait.WithInterval(time.Second)); err != nil {
				t.Fatal(err)
			}
			t.Logf("native job %s remains pending while pod %s runs on %s", nativeID, pod.Name, node)

			if _, err := execInPod(ctx, config, pod, "touch", "/tmp/finish"); err != nil {
				t.Fatalf("finish Kubernetes work: %v", err)
			}
			pod, err = waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), func(p *corev1.Pod) bool {
				return p.Status.Phase == corev1.PodSucceeded
			})
			if err != nil {
				t.Fatalf("Kubernetes workload did not finish: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				output, err = querySlurmJob(ctx, config, crClient, nativeID)
				if err != nil {
					return false, err
				}
				state, err := slurmJobField(output, "JobState")
				if err == nil && state != "PENDING" && state != "RUNNING" {
					return false, fmt.Errorf("native job reached %s: %s", state, output)
				}
				return state == "RUNNING", err
			}, wait.WithContext(ctx), wait.WithTimeout(slurmBridgeReadinessTimeout), wait.WithInterval(time.Second)); err != nil {
				t.Fatalf("native job did not start after Kubernetes work ended: %v", err)
			}
			if allocatedNode, err := slurmJobNodeList(output); err != nil || allocatedNode != node {
				t.Fatalf("native job ran on %q, want %q: %v", allocatedNode, node, err)
			}
			t.Logf("native job %s started on %s after Kubernetes work ended", nativeID, node)
			return ctx
		}).Feature()
}
