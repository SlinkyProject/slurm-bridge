// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestLeaderWorkerSetNativeScheduling(t *testing.T) {
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_ = testEnv.Test(t,
		withSuiteLabel(testLeaderWorkerSetNativeLifecycle(), "scheduling"),
		withSuiteLabel(testLeaderWorkerSetRestartBudget(), "scheduling"),
	)
}

func nativeLWS(name string, replicas, size int32) *lwsv1.LeaderWorkerSet {
	leader := slurmTestPodTemplate([]string{"sh", "-c", "sleep 86400"})
	worker := leader.DeepCopy()
	leader.Spec.RestartPolicy = corev1.RestartPolicyAlways
	worker.Spec.RestartPolicy = corev1.RestartPolicyAlways
	return &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: slurmBridgeNamespace},
		Spec: lwsv1.LeaderWorkerSetSpec{
			Replicas:      ptr.To(replicas),
			GroupIdentity: lwsv1.GroupIdentityOrdinal,
			StartupPolicy: lwsv1.LeaderCreatedStartupPolicy,
			Scheduling:    &lwsv1.LeaderWorkerSetScheduling{},
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size: ptr.To(size), RestartPolicy: lwsv1.RecreateGroupOnPodRestart,
				LeaderTemplate: &leader, WorkerTemplate: *worker,
			},
		},
	}
}

func testLeaderWorkerSetNativeLifecycle() types.Feature {
	lws := nativeLWS(envconf.RandomName("lws-native-e2e", 30), 2, 2)
	var pods []corev1.Pod
	return features.New("LeaderWorkerSet generated PodGroup lifecycle").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireKubernetesPodGroupAPI(t, config, podGroupV1Beta1)
			c := nativeLWSClient(t, config)
			if err := c.Create(ctx, lws); err != nil {
				t.Fatalf("create native LeaderWorkerSet: %v", err)
			}
			return ctx
		}).
		Assess("LWS creates an independent gang for each replica", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			pods = waitForNativeLWSPods(ctx, t, config, lws, 2, 2)
			return ctx
		}).
		Assess("a recreated leader gets a new gang and allocation", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c := nativeLWSClient(t, config)
			leader := &corev1.Pod{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: lws.Namespace, Name: lws.Name + "-0"}, leader); err != nil {
				t.Fatal(err)
			}
			oldGroup := *leader.Spec.SchedulingGroup.PodGroupName
			oldJob := leader.Labels[slurmJobIDLabel]
			if err := c.Delete(ctx, leader, client.Preconditions{UID: &leader.UID}); err != nil {
				t.Fatal(err)
			}
			waitForLWSReplacement(ctx, t, c, leader)
			previous := pods
			pods = waitForNativeLWSPods(ctx, t, config, lws, 2, 2)
			for i := range pods {
				pod := &pods[i]
				if pod.Labels[lwsv1.GroupIndexLabelKey] == "0" {
					if *pod.Spec.SchedulingGroup.PodGroupName == oldGroup || pod.Labels[slurmJobIDLabel] == oldJob {
						t.Errorf("replacement pod %s reused the old gang or allocation", pod.Name)
					}
				} else {
					for _, old := range previous {
						if old.Name == pod.Name && (old.UID != pod.UID || old.Labels[slurmJobIDLabel] != pod.Labels[slurmJobIDLabel]) {
							t.Errorf("recreating replica 0 changed replica 1 pod %s", pod.Name)
						}
					}
				}
			}
			assertSlurmJobsGone(ctx, t, config, c, []string{oldJob})
			waitForLWSObjectGone(ctx, t, c, &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{Namespace: lws.Namespace, Name: oldGroup}})
			return ctx
		}).
		Assess("replica scaling releases removed gangs", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c := nativeLWSClient(t, config)
			previous := pods
			updateNativeLWS(ctx, t, c, lws, func() { lws.Spec.Replicas = ptr.To[int32](1) })
			pods = waitForNativeLWSPods(ctx, t, config, lws, 1, 2)
			for i := range previous {
				if previous[i].Labels[lwsv1.GroupIndexLabelKey] == "1" && previous[i].Labels[lwsv1.WorkerIndexLabelKey] == "0" {
					assertSlurmJobsGone(ctx, t, config, c, []string{previous[i].Labels[slurmJobIDLabel]})
					waitForLWSObjectGone(ctx, t, c, &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{
						Namespace: lws.Namespace, Name: *previous[i].Spec.SchedulingGroup.PodGroupName,
					}})
				}
			}
			updateNativeLWS(ctx, t, c, lws, func() { lws.Spec.Replicas = ptr.To[int32](2) })
			pods = waitForNativeLWSPods(ctx, t, config, lws, 2, 2)
			updateNativeLWS(ctx, t, c, lws, func() { lws.Spec.Replicas = ptr.To[int32](1) })
			pods = waitForNativeLWSPods(ctx, t, config, lws, 1, 2)
			return ctx
		}).
		Assess("size changes roll out gangs with the new minimum", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c := nativeLWSClient(t, config)
			for _, size := range []int32{3, 2} {
				oldJobs := podSlurmJobIDs(pods)
				oldGroup := *pods[0].Spec.SchedulingGroup.PodGroupName
				updateNativeLWS(ctx, t, c, lws, func() { lws.Spec.LeaderWorkerTemplate.Size = ptr.To(size) })
				pods = waitForNativeLWSPods(ctx, t, config, lws, 1, size)
				assertSlurmJobsGone(ctx, t, config, c, oldJobs)
				waitForLWSObjectGone(ctx, t, c, &schedulingv1beta1.PodGroup{ObjectMeta: metav1.ObjectMeta{Namespace: lws.Namespace, Name: oldGroup}})
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			cleanupNativeLWS(ctx, t, config, lws)
			return ctx
		}).Feature()
}

func testLeaderWorkerSetRestartBudget() types.Feature {
	lws := nativeLWS(envconf.RandomName("lws-budget-e2e", 30), 1, 2)
	lws.Spec.PodTerminationPolicy = lwsv1.ParallelPodTerminationPolicy
	lws.Spec.LeaderWorkerTemplate.MaxGroupRestarts = ptr.To[int32](0)
	lws.Spec.LeaderWorkerTemplate.RestartBackoff = &lwsv1.RestartBackoff{BaseSeconds: ptr.To[int32](1), CapSeconds: ptr.To[int32](1)}
	lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Command = []string{"sh", "-c", "while [ ! -e /tmp/fail ]; do sleep 1; done; exit 1"}
	var pods []corev1.Pod
	return features.New("LeaderWorkerSet bounded recovery and parallel termination").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			requireKubernetesPodGroupAPI(t, config, podGroupV1Beta1)
			if err := nativeLWSClient(t, config).Create(ctx, lws); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Assess("exhausted groups release Slurm resources while retaining terminal pods", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c := nativeLWSClient(t, config)
			pods = waitForNativeLWSPods(ctx, t, config, lws, 1, 2)
			leader := &corev1.Pod{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: lws.Namespace, Name: lws.Name + "-0"}, leader); err != nil {
				t.Fatal(err)
			}
			if _, err := execInPod(ctx, config, leader, "touch", "/tmp/fail"); err != nil {
				t.Fatalf("trigger leader failure: %v", err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				if err := c.Get(ctx, client.ObjectKeyFromObject(lws), lws); err != nil {
					return false, err
				}
				return meta.IsStatusConditionTrue(lws.Status.Conditions, string(lwsv1.LeaderWorkerSetDegraded)), nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(time.Second)); err != nil {
				t.Fatalf("restart budget did not become exhausted: %v", err)
			}
			for i := range pods {
				if err := wait.For(func(ctx context.Context) (bool, error) {
					pod := &corev1.Pod{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(&pods[i]), pod); err != nil {
						return false, err
					}
					return pod.DeletionTimestamp != nil && (pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed) &&
						slices.Contains(pod.Finalizers, lwsv1.GroupRestartBudgetCleanupFinalizer) &&
						!slices.Contains(pod.Finalizers, wellknown.FinalizerScheduler), nil
				}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(time.Second)); err != nil {
					t.Fatalf("retained pod %s did not release its bridge finalizer: %v", pods[i].Name, err)
				}
			}
			assertSlurmJobsGone(ctx, t, config, c, podSlurmJobIDs(pods))
			return ctx
		}).
		Assess("explicit recovery creates a fresh gang", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			c := nativeLWSClient(t, config)
			leader := &corev1.Pod{}
			if err := c.Get(ctx, client.ObjectKey{Namespace: lws.Namespace, Name: lws.Name + "-0"}, leader); err != nil {
				t.Fatal(err)
			}
			base := leader.DeepCopy()
			leader.Annotations[lwsv1.GroupRestartBudgetRecoverAnnotationKey] = "true"
			if err := c.Patch(ctx, leader, client.MergeFrom(base)); err != nil {
				t.Fatal(err)
			}
			waitForLWSReplacement(ctx, t, c, leader)
			pods = waitForNativeLWSPods(ctx, t, config, lws, 1, 2)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			cleanupNativeLWS(ctx, t, config, lws)
			return ctx
		}).Feature()
}

func nativeLWSClient(t *testing.T, config *envconf.Config) client.Client {
	t.Helper()
	c, err := getControllerRuntimeClient(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := schedulingv1beta1.AddToScheme(c.Scheme()); err != nil {
		t.Fatal(err)
	}
	return c
}

func waitForNativeLWSPods(ctx context.Context, t *testing.T, config *envconf.Config, lws *lwsv1.LeaderWorkerSet, replicas, size int32) []corev1.Pod {
	t.Helper()
	c := nativeLWSClient(t, config)
	var pods []corev1.Pod
	if err := wait.For(func(ctx context.Context) (bool, error) {
		list := &corev1.PodList{}
		if err := c.List(ctx, list, client.InNamespace(lws.Namespace), client.MatchingLabels{lwsv1.SetNameLabelKey: lws.Name}); err != nil {
			return false, err
		}
		pods = list.Items
		if len(pods) != int(replicas*size) {
			return false, nil
		}
		for i := range pods {
			pod := &pods[i]
			if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !podHasSlurmAllocation(pod) ||
				pod.Annotations[lwsv1.SizeAnnotationKey] != strconv.Itoa(int(size)) {
				return false, nil
			}
		}
		return true, nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(time.Second)); err != nil {
		t.Fatalf("native LWS replicas were not allocated: %v", err)
	}
	groups := map[string][]corev1.Pod{}
	for i := range pods {
		pod := &pods[i]
		assertBridgePod(t, ctx, c, pod)
		if pod.Spec.SchedulingGroup == nil || pod.Spec.SchedulingGroup.PodGroupName == nil {
			t.Fatalf("LWS did not assign pod %s a PodGroup", pod.Name)
		}
		name := *pod.Spec.SchedulingGroup.PodGroupName
		groups[name] = append(groups[name], *pod)
	}
	if len(groups) != int(replicas) || len(podSlurmJobIDs(pods)) != int(replicas) {
		t.Fatalf("LWS has %d gangs and %d allocations, want %d", len(groups), len(podSlurmJobIDs(pods)), replicas)
	}
	for name, members := range groups {
		jobs := podSlurmJobIDs(members)
		if len(members) != int(size) || len(jobs) != 1 {
			t.Fatalf("gang %s has %d pods and %d allocations, want %d and 1", name, len(members), len(jobs), size)
		}
		assertSlurmNodeCount(ctx, t, config, c, jobs[0], int(size))
		pg := &schedulingv1beta1.PodGroup{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: lws.Namespace, Name: name}, pg); err != nil {
			t.Fatal(err)
		}
		if pg.Spec.SchedulingPolicy.Gang == nil || pg.Spec.SchedulingPolicy.Gang.MinCount != size {
			t.Fatalf("PodGroup %s does not require %d members", name, size)
		}
		if pg.Spec.WorkloadRef == nil {
			t.Fatalf("PodGroup %s has no generated Workload reference", name)
		}
		workload := &schedulingv1beta1.Workload{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: lws.Namespace, Name: pg.Spec.WorkloadRef.WorkloadName}, workload); err != nil {
			t.Fatal(err)
		}
		if !metav1.IsControlledBy(workload, lws) {
			t.Fatalf("Workload %s is not owned by LWS %s", workload.Name, lws.Name)
		}
		assertKubernetesPodGroupScheduled(ctx, t, c, newKubernetesPodGroup(podGroupV1Beta1, name, workload.Name))
	}
	return pods
}

func updateNativeLWS(ctx context.Context, t *testing.T, c client.Client, lws *lwsv1.LeaderWorkerSet, update func()) {
	t.Helper()
	if err := c.Get(ctx, client.ObjectKeyFromObject(lws), lws); err != nil {
		t.Fatal(err)
	}
	base := lws.DeepCopy()
	update()
	if err := c.Patch(ctx, lws, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
}

func waitForLWSObjectGone(ctx context.Context, t *testing.T, c client.Client, object client.Object) {
	t.Helper()
	if err := wait.For(func(ctx context.Context) (bool, error) {
		err := c.Get(ctx, client.ObjectKeyFromObject(object), object)
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(time.Second)); err != nil {
		t.Errorf("%T %s was not deleted: %v", object, object.GetName(), err)
	}
}

func cleanupNativeLWS(ctx context.Context, t *testing.T, config *envconf.Config, lws *lwsv1.LeaderWorkerSet) {
	t.Helper()
	captureReleaseSignalDiagnostics(t, "native LeaderWorkerSet", slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "lws-system")
	if !e2eCleanupEnabled(t) {
		return
	}
	c := nativeLWSClient(t, config)
	podList := &corev1.PodList{}
	if err := c.List(ctx, podList, client.InNamespace(lws.Namespace), client.MatchingLabels{lwsv1.SetNameLabelKey: lws.Name}); err != nil {
		t.Error(err)
		return
	}
	groups := &schedulingv1beta1.PodGroupList{}
	workloads := &schedulingv1beta1.WorkloadList{}
	options := []client.ListOption{client.InNamespace(lws.Namespace), client.MatchingLabels{lwsv1.SetNameLabelKey: lws.Name}}
	if err := c.List(ctx, groups, options...); err != nil {
		t.Error(err)
	}
	if err := c.List(ctx, workloads, options...); err != nil {
		t.Error(err)
	}
	deleteObject(t, ctx, c, lws)
	pods := make([]*corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		pods = append(pods, &podList.Items[i])
	}
	deletePodsAndAssertCleanup(ctx, t, config, c, pods...)
	waitForLWSObjectGone(ctx, t, c, lws)
	for i := range groups.Items {
		waitForLWSObjectGone(ctx, t, c, &groups.Items[i])
	}
	for i := range workloads.Items {
		waitForLWSObjectGone(ctx, t, c, &workloads.Items[i])
	}
}

func waitForLWSReplacement(ctx context.Context, t *testing.T, c client.Client, previous *corev1.Pod) {
	t.Helper()
	if err := wait.For(func(ctx context.Context) (bool, error) {
		pod := &corev1.Pod{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(previous), pod); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return pod.UID != previous.UID && pod.DeletionTimestamp == nil && podHasSlurmAllocation(pod), nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(time.Second)); err != nil {
		t.Fatalf("replacement leader was not allocated: %v", err)
	}
}
