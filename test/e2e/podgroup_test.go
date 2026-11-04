// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func testKubernetesPodGroupScheduling(api kubernetesPodGroupAPI) types.Feature {
	featureName := "Kubernetes PodGroup workload (" + string(api) + ")"
	workloadName := envconf.RandomName("workload-e2e", 40)
	jobName := envconf.RandomName("podgroup-job-e2e", 40)
	podGroupName := jobName + "-workers"
	var slurmJobIDs []string
	workload := newKubernetesWorkload(api, workloadName, "batch", "Job", jobName)
	podGroup := newKubernetesPodGroup(api, podGroupName, workloadName)
	template := slurmTestPodTemplate([]string{"sh", "-c", "sleep 10"})
	template.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(podGroupName)}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: slurmBridgeNamespace},
		Spec: batchv1.JobSpec{
			Parallelism:  ptr.To[int32](2),
			Completions:  ptr.To[int32](2),
			BackoffLimit: ptr.To[int32](0),
			Template:     template,
		},
	}

	return features.New(featureName).
		WithLabel("workload-api", string(api)).
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireKubernetesPodGroupAPI(t, config, api)
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			for _, object := range []client.Object{workload, podGroup, job} {
				if err := crClient.Create(ctx, object); err != nil {
					t.Fatalf("create %T %s: %v", object, object.GetName(), err)
				}
			}
			return ctx
		}).
		Assess("gang pods share one two-node Slurm job", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("PodGroup pods were not allocated: %v", err)
			}
			slurmJobIDs = podSlurmJobIDs(pods)
			if len(slurmJobIDs) != 1 {
				t.Fatalf("PodGroup pods have %d Slurm jobs, want 1: %v", len(slurmJobIDs), slurmJobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, slurmJobIDs[0], 2)
			nodes := map[string]struct{}{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				nodes[pods[i].Spec.NodeName] = struct{}{}
			}
			if len(nodes) != 2 {
				t.Errorf("PodGroup pods use %d nodes, want 2: %v", len(nodes), nodes)
			}
			assertKubernetesPodGroupScheduled(ctx, t, crClient, podGroup)
			return ctx
		}).
		Assess("gang Job completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 2, podFinishedAndReleased); err != nil {
				t.Fatalf("PodGroup Job did not complete finalizer processing: %v", err)
			}
			assertSlurmJobsGone(ctx, t, config, crClient, slurmJobIDs)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, featureName,
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			for _, object := range []client.Object{job, podGroup, workload} {
				deleteObject(t, ctx, crClient, object)
			}
			return ctx
		}).
		Feature()
}

func testSchedulerPluginsPodGroupScheduling() types.Feature {
	podGroupName := envconf.RandomName("coscheduling-e2e", 40)
	var slurmJobIDs []string
	podGroup := &schedv1alpha1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: podGroupName, Namespace: slurmBridgeNamespace},
		Spec: schedv1alpha1.PodGroupSpec{
			MinMember: 2,
			MinResources: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(testCPU),
				corev1.ResourceMemory: resource.MustParse(testMemory),
			},
		},
	}
	pods := []*corev1.Pod{
		slurmTestPod(slurmBridgeNamespace, podGroupName+"-0", []string{"sh", "-c", "sleep 10"}),
		slurmTestPod(slurmBridgeNamespace, podGroupName+"-1", []string{"sh", "-c", "sleep 10"}),
	}
	for _, pod := range pods {
		pod.Labels = map[string]string{schedv1alpha1.PodGroupLabel: podGroupName}
	}

	return features.New("Scheduler-plugins PodGroup workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, podGroup); err != nil {
				t.Fatalf("create scheduler-plugins PodGroup: %v", err)
			}
			for _, pod := range pods {
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create PodGroup pod %s: %v", pod.Name, err)
				}
			}
			return ctx
		}).
		Assess("coscheduled pods share one Slurm allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			observed, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{schedv1alpha1.PodGroupLabel: podGroupName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("scheduler-plugins PodGroup pods were not allocated: %v", err)
			}
			slurmJobIDs = podSlurmJobIDs(observed)
			if len(slurmJobIDs) != 1 {
				t.Fatalf("PodGroup pods have %d Slurm jobs, want 1: %v", len(slurmJobIDs), slurmJobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, slurmJobIDs[0], 2)
			for i := range observed {
				assertBridgePod(t, ctx, crClient, &observed[i])
			}
			return ctx
		}).
		Assess("coscheduled pods complete", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{schedv1alpha1.PodGroupLabel: podGroupName}, 2, podFinishedAndReleased); err != nil {
				t.Fatalf("scheduler-plugins PodGroup pods did not complete finalizer processing: %v", err)
			}
			assertSlurmJobsGone(ctx, t, config, crClient, slurmJobIDs)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "scheduler-plugins PodGroup workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "scheduler-plugins")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			for _, pod := range pods {
				deleteObject(t, ctx, crClient, pod)
			}
			deleteObject(t, ctx, crClient, podGroup)
			return ctx
		}).
		Feature()
}

// TestPodGroupLateArrivals preserves running gang members when new members arrive.
// Run serially: this test pauses the scheduler and requires six idle bridge nodes.
func TestPodGroupLateArrivals(t *testing.T) {
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	feature := features.New("PodGroup late arrivals preserve running members").
		Assess("late arrivals cannot relabel or terminate the original gang", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireKubernetesPodGroupAPI(t, config, podGroupV1Beta1)
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			controller, err := getSlurmControllerPod(ctx, crClient)
			if err != nil {
				t.Fatal(err)
			}
			output, err := execInPod(ctx, config, controller,
				"sinfo", "--noheader", "--Node", "--partition="+slurmBridgePartition, "--format=%T")
			if err != nil {
				t.Fatalf("query idle Slurm nodes: %v", err)
			}
			states := strings.Fields(output)
			if len(states) != 6 {
				t.Fatalf("Slurm partition %s has %d nodes, want six idle nodes: %s", slurmBridgePartition, len(states), output)
			}
			for _, state := range states {
				if state != "idle" {
					t.Fatalf("Slurm node state = %q, want idle: %s", state, output)
				}
			}

			schedulers := &appsv1.DeploymentList{}
			if err := crClient.List(ctx, schedulers, client.MatchingLabels{"app.kubernetes.io/name": slurmBridgeScheduler}); err != nil {
				t.Fatalf("list scheduler deployments: %v", err)
			}
			if len(schedulers.Items) != 1 {
				t.Fatalf("found %d scheduler deployments, want 1", len(schedulers.Items))
			}
			scheduler := &schedulers.Items[0]
			schedulerKey := client.ObjectKeyFromObject(scheduler)
			originalReplicas := ptr.Deref(scheduler.Spec.Replicas, 1)
			scaleScheduler := func(ctx context.Context, replicas int32) error {
				if err := crClient.Get(ctx, schedulerKey, scheduler); err != nil {
					return err
				}
				before := scheduler.DeepCopy()
				scheduler.Spec.Replicas = ptr.To(replicas)
				if err := crClient.Patch(ctx, scheduler, client.MergeFrom(before)); err != nil {
					return err
				}
				return wait.For(func(ctx context.Context) (bool, error) {
					if err := crClient.Get(ctx, schedulerKey, scheduler); err != nil {
						return false, err
					}
					if replicas == 0 {
						pods := &corev1.PodList{}
						if err := crClient.List(ctx, pods, client.InNamespace(schedulerKey.Namespace),
							client.MatchingLabels(scheduler.Spec.Selector.MatchLabels)); err != nil {
							return false, err
						}
						return len(pods.Items) == 0, nil
					}
					return scheduler.Status.ObservedGeneration >= scheduler.Generation &&
						scheduler.Status.ReadyReplicas == replicas, nil
				}, wait.WithContext(ctx), wait.WithTimeout(slurmBridgeReadinessTimeout), wait.WithInterval(time.Second))
			}
			name := envconf.RandomName("gang", 32)
			podGroup := newKubernetesPodGroup(podGroupV1Beta1, name, "")
			// Standalone members need no Workload or controller reference.
			unstructured.RemoveNestedField(podGroup.Object, "spec", "workloadRef")
			var pods []*corev1.Pod
			t.Cleanup(func() {
				cleanupCtx := context.WithoutCancel(ctx)
				// Restore shared infrastructure even when workload cleanup is disabled.
				if err := scaleScheduler(cleanupCtx, originalReplicas); err != nil {
					t.Errorf("restore scheduler replicas to %d: %v", originalReplicas, err)
				}
				captureReleaseSignalDiagnostics(t, "PodGroup late arrivals", slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
				if e2eCleanupEnabled(t) {
					deletePodsAndAssertCleanup(cleanupCtx, t, config, crClient, pods...)
					deleteObject(t, cleanupCtx, crClient, podGroup)
				}
			})
			if err := scaleScheduler(ctx, 0); err != nil {
				t.Fatalf("pause scheduler: %v", err)
			}
			if err := crClient.Create(ctx, podGroup); err != nil {
				t.Fatalf("create PodGroup: %v", err)
			}
			createMember := func() {
				t.Helper()
				pod := slurmTestPod(slurmBridgeNamespace, fmt.Sprintf("%s-%d", name, len(pods)), []string{"sleep", "7200"})
				pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(name)}
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create gang member %s: %v", pod.Name, err)
				}
				pods = append(pods, pod)
			}
			for range 3 {
				createMember()
			}
			if err := scaleScheduler(ctx, 1); err != nil {
				t.Fatalf("resume scheduler: %v", err)
			}
			for i, pod := range pods {
				observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), func(p *corev1.Pod) bool {
					return podHasSlurmAllocation(p) && p.Status.Phase == corev1.PodRunning
				})
				if err != nil {
					t.Fatalf("initial member %s did not run: %v", pod.Name, err)
				}
				pods[i] = observed
				assertBridgePod(t, ctx, crClient, observed)
			}
			jobID := pods[0].Labels[slurmJobIDLabel]
			nodes := map[string]bool{}
			for _, pod := range pods {
				if pod.Labels[slurmJobIDLabel] != jobID {
					t.Fatalf("initial member %s has job ID %q, want %q", pod.Name, pod.Labels[slurmJobIDLabel], jobID)
				}
				nodes[pod.Spec.NodeName] = true
			}
			if len(nodes) != 3 {
				t.Fatalf("initial gang uses %d nodes, want 3", len(nodes))
			}
			assertSlurmNodeCount(ctx, t, config, crClient, jobID, 3)
			output, err = querySlurmJob(ctx, config, crClient, jobID)
			if err != nil {
				t.Fatal(err)
			}
			if exclusive, err := slurmJobField(output, "OverSubscribe"); err != nil || exclusive != "NO" {
				t.Fatalf("initial job default OverSubscribe = %q, want NO (exclusive): %v", exclusive, err)
			}

			// Check invariants on every poll, including while a replacement job is pending.
			checkOriginals := func(ctx context.Context) error {
				for _, original := range pods[:3] {
					current := &corev1.Pod{}
					if err := crClient.Get(ctx, client.ObjectKeyFromObject(original), current); err != nil {
						return fmt.Errorf("get original member %s: %w", original.Name, err)
					}
					if current.UID != original.UID || current.Labels[slurmJobIDLabel] != jobID ||
						current.Spec.NodeName != original.Spec.NodeName || current.DeletionTimestamp != nil ||
						current.Status.Phase != corev1.PodRunning {
						return fmt.Errorf("original member %s changed: UID=%s jobID=%q node=%q phase=%s deleting=%v; want UID=%s jobID=%q node=%q Running and not deleting",
							current.Name, current.UID, current.Labels[slurmJobIDLabel], current.Spec.NodeName, current.Status.Phase,
							current.DeletionTimestamp, original.UID, jobID, original.Spec.NodeName)
					}
				}
				output, err := querySlurmJob(ctx, config, crClient, jobID)
				if err != nil {
					return err
				}
				if state, err := slurmJobField(output, "JobState"); err != nil || state != "RUNNING" {
					return fmt.Errorf("original Slurm job %s state=%q, want RUNNING: %w", jobID, state, err)
				}
				return nil
			}
			createMember()
			deadline := time.Now().Add(15 * time.Second)
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := checkOriginals(ctx); err != nil {
					return false, err
				}
				fourth := &corev1.Pod{}
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(pods[3]), fourth); err != nil {
					return false, err
				}
				if id := fourth.Labels[slurmJobIDLabel]; fourth.Status.Phase != corev1.PodPending ||
					fourth.Spec.NodeName != "" || fourth.DeletionTimestamp != nil || (id != "" && id != jobID) {
					return false, fmt.Errorf("fourth member phase=%s jobID=%q node=%q deleting=%v, want Pending, unbound and not deleting with no job ID or original job ID %q",
						fourth.Status.Phase, id, fourth.Spec.NodeName, fourth.DeletionTimestamp, jobID)
				}
				return time.Now().After(deadline), nil
			}, wait.WithContext(ctx), wait.WithTimeout(time.Minute), wait.WithInterval(time.Second)); err != nil {
				t.Fatalf("single late arrival violated gang isolation: %v", err)
			}

			createMember()
			// Allow scheduler retries and controller reconciliation to expose delayed deletion.
			deadline = time.Now().Add(time.Minute)
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := checkOriginals(ctx); err != nil {
					return false, err
				}
				return time.Now().After(deadline), nil
			}, wait.WithContext(ctx), wait.WithTimeout(2*time.Minute), wait.WithInterval(time.Second)); err != nil {
				t.Fatalf("late gang quorum relabeled or terminated running members: %v", err)
			}
			return ctx
		}).Feature()
	_ = testEnv.Test(t, feature)
}

// TestPodGroupLateArrivalsPending grows the original job before Slurm allocates nodes.
func TestPodGroupLateArrivalsPending(t *testing.T) {
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	feature := features.New("PodGroup late arrivals grow pending allocation").
		Assess("late members increase the node count of the same pending job", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireKubernetesPodGroupAPI(t, config, podGroupV1Beta1)
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			controller, err := getSlurmControllerPod(ctx, crClient)
			if err != nil {
				t.Fatal(err)
			}
			output, err := execInPod(ctx, config, controller,
				"sinfo", "--noheader", "--Node", "--partition="+slurmBridgePartition, "--format=%N")
			if err != nil {
				t.Fatalf("query bridge nodes: %v", err)
			}
			nodes := strings.Fields(output)
			if len(nodes) < 4 {
				t.Fatalf("Slurm partition %s has %d nodes, want at least 4", slurmBridgePartition, len(nodes))
			}
			name := envconf.RandomName("pending-gang", 32)
			// A private DOWN partition accepts submissions but cannot allocate nodes.
			if _, err := execInPod(ctx, config, controller, "scontrol", "create",
				"PartitionName="+name, "Nodes="+strings.Join(nodes, ","), "Default=NO", "State=DOWN"); err != nil {
				t.Fatalf("create pending-only partition: %v", err)
			}
			podGroup := newKubernetesPodGroup(podGroupV1Beta1, name, "")
			unstructured.RemoveNestedField(podGroup.Object, "spec", "workloadRef")
			podGroup.SetAnnotations(map[string]string{wellknown.AnnotationPartition: name})
			var pods []*corev1.Pod
			t.Cleanup(func() {
				captureReleaseSignalDiagnostics(t, "PodGroup pending late arrivals", slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
				if !e2eCleanupEnabled(t) {
					return
				}
				cleanupCtx, cancel := context.WithTimeout(context.Background(), slurmCleanupTimeout)
				defer cancel()
				deletePodsAndAssertCleanup(cleanupCtx, t, config, crClient, pods...)
				deleteObject(t, cleanupCtx, crClient, podGroup)
				if _, err := execInPod(cleanupCtx, config, controller, "scontrol", "delete", "PartitionName="+name); err != nil {
					t.Errorf("delete pending-only partition: %v", err)
				}
			})
			if err := crClient.Create(ctx, podGroup); err != nil {
				t.Fatalf("create PodGroup: %v", err)
			}
			createMember := func() {
				t.Helper()
				pod := slurmTestPod(slurmBridgeNamespace, fmt.Sprintf("%s-%d", name, len(pods)), []string{"sleep", "7200"})
				pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(name)}
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create gang member %s: %v", pod.Name, err)
				}
				pods = append(pods, pod)
			}
			createMember()
			createMember()
			first, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pods[0]), func(p *corev1.Pod) bool {
				return p.Labels[slurmJobIDLabel] != ""
			})
			if err != nil {
				t.Fatalf("initial gang did not receive a Slurm job ID: %v", err)
			}
			jobID := first.Labels[slurmJobIDLabel]
			for want := 2; want <= 4; want++ {
				if want > 2 {
					createMember()
				}
				var lastOutput string
				if err := wait.For(func(ctx context.Context) (bool, error) {
					lastOutput, err = querySlurmJob(ctx, config, crClient, jobID)
					if err != nil {
						return false, err
					}
					state, err := slurmJobField(lastOutput, "JobState")
					if err != nil {
						return false, err
					}
					if state != "PENDING" || (!strings.Contains(" "+lastOutput+" ", " NodeList=(null) ") && !strings.Contains(" "+lastOutput+" ", " NodeList= ")) {
						return false, fmt.Errorf("job %s must stay pending without allocated nodes: %s", jobID, lastOutput)
					}
					count, err := slurmJobField(lastOutput, "NumNodes")
					if err != nil || (count != fmt.Sprint(want) && count != fmt.Sprintf("%d-%d", want, want)) {
						return false, err
					}
					for _, pod := range pods {
						current := &corev1.Pod{}
						if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
							return false, err
						}
						if id := current.Labels[slurmJobIDLabel]; id != "" && id != jobID {
							return false, fmt.Errorf("member %s received replacement job %q, want %s", pod.Name, id, jobID)
						}
						if current.UID != pod.UID || current.DeletionTimestamp != nil || current.Spec.NodeName != "" || current.Status.Phase != corev1.PodPending {
							return false, fmt.Errorf("member %s changed identity or left Pending: %+v", pod.Name, current)
						}
						if current.Labels[slurmJobIDLabel] == "" {
							return false, nil
						}
					}
					return true, nil
				}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(time.Second)); err != nil {
					t.Fatalf("pending job %s did not grow to %d nodes: %v; last job: %s", jobID, want, err, lastOutput)
				}
			}
			return ctx
		}).Feature()
	_ = testEnv.Test(t, feature)
}
