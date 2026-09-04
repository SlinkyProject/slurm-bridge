// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/types"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

const (
	testContainerImage = "busybox:stable"
	testCPU            = "1"
	testMemory         = "100Mi"
)

func addReleaseSignalSchemes(scheme *runtime.Scheme) error {
	adders := []func(*runtime.Scheme) error{
		resourcev1.AddToScheme,
		schedulingv1alpha2.AddToScheme,
		jobsetv1alpha2.AddToScheme,
		lwsv1.AddToScheme,
		schedv1alpha1.AddToScheme,
	}
	for _, add := range adders {
		if err := add(scheme); err != nil {
			return err
		}
	}
	return nil
}

func slurmTestResources(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	}
}

func slurmTestPod(namespace, name string, command []string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			SchedulerName: slurmBridgeScheduler,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:      "worker",
				Image:     testContainerImage,
				Command:   command,
				Resources: slurmTestResources(testCPU, testMemory),
			}},
		},
	}
}

func slurmTestPodTemplate(command []string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: slurmTestPod("", "", command).Spec}
}

func getSlurmControllerPod(ctx context.Context, crClient client.Client) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	if err := crClient.Get(ctx, client.ObjectKey{
		Namespace: slurmNamespace,
		Name:      slurmControllerPodName,
	}, pod); err != nil {
		return nil, fmt.Errorf("get Slurm controller pod: %w", err)
	}
	return pod, nil
}

func querySlurmJob(
	ctx context.Context,
	config *envconf.Config,
	crClient client.Client,
	jobID string,
) (string, error) {
	controllerPod, err := getSlurmControllerPod(ctx, crClient)
	if err != nil {
		return "", err
	}
	output, err := execInPod(ctx, config, controllerPod,
		"scontrol", "show", "job", jobID, "--oneliner")
	if err != nil {
		return "", fmt.Errorf("query Slurm job %s: %w", jobID, err)
	}
	return output, nil
}

func waitForSlurmJobGone(
	ctx context.Context,
	config *envconf.Config,
	crClient client.Client,
	jobID string,
) error {
	if jobID == "" {
		return nil
	}
	controllerPod, err := getSlurmControllerPod(ctx, crClient)
	if err != nil {
		return err
	}
	return wait.For(func(ctx context.Context) (bool, error) {
		output, err := execInPod(ctx, config, controllerPod,
			"squeue", "--noheader", "--format=%i")
		if err != nil {
			return false, err
		}
		return !slices.Contains(strings.Fields(output), jobID), nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second))
}

func waitForPod(
	ctx context.Context,
	crClient client.Client,
	key client.ObjectKey,
	predicate func(*corev1.Pod) bool,
) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := wait.For(func(ctx context.Context) (bool, error) {
		if err := crClient.Get(ctx, key, pod); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if pod.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("pod %s failed: %s", key, pod.Status.Message)
		}
		return predicate(pod), nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(3*time.Second))
	return pod, err
}

func waitForLabeledPods(
	ctx context.Context,
	crClient client.Client,
	namespace string,
	labels map[string]string,
	count int,
	predicate func(*corev1.Pod) bool,
) ([]corev1.Pod, error) {
	var pods []corev1.Pod
	err := wait.For(func(ctx context.Context) (bool, error) {
		podList := &corev1.PodList{}
		if err := crClient.List(ctx, podList,
			client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
			return false, err
		}
		pods = podList.Items
		if len(pods) != count {
			return false, nil
		}
		for i := range pods {
			if pods[i].Status.Phase == corev1.PodFailed {
				return false, fmt.Errorf("pod %s failed: %s", pods[i].Name, pods[i].Status.Message)
			}
			if !predicate(&pods[i]) {
				return false, nil
			}
		}
		return true, nil
	}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(3*time.Second))
	return pods, err
}

func podHasSlurmAllocation(pod *corev1.Pod) bool {
	return pod.Spec.NodeName != "" && pod.Labels[slurmJobIDLabel] != ""
}

func podFinished(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded
}

func assertBridgePod(t *testing.T, ctx context.Context, crClient client.Client, pod *corev1.Pod) {
	t.Helper()
	if pod.Spec.SchedulerName != slurmBridgeScheduler {
		t.Errorf("pod %s/%s scheduler is %q, want %q",
			pod.Namespace, pod.Name, pod.Spec.SchedulerName, slurmBridgeScheduler)
	}
	node := &corev1.Node{}
	if err := crClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
		t.Errorf("get node %s: %v", pod.Spec.NodeName, err)
		return
	}
	if node.Labels[slurmBridgeWorkerLabel] != "worker" {
		t.Errorf("pod %s/%s ran on non-bridge node %s", pod.Namespace, pod.Name, pod.Spec.NodeName)
	}
}

func assertSlurmNodeCount(
	ctx context.Context,
	t *testing.T,
	config *envconf.Config,
	crClient client.Client,
	jobID string,
	want int,
) {
	t.Helper()
	output, err := querySlurmJob(ctx, config, crClient, jobID)
	if err != nil {
		t.Error(err)
		return
	}
	got, err := slurmJobField(output, "NumNodes")
	if err != nil {
		t.Error(err)
		return
	}
	if got != fmt.Sprint(want) {
		t.Errorf("Slurm job %s has NumNodes=%s, want %d", jobID, got, want)
	}
}

func podSlurmJobIDs(pods []corev1.Pod) []string {
	seen := map[string]struct{}{}
	for i := range pods {
		if jobID := pods[i].Labels[slurmJobIDLabel]; jobID != "" {
			seen[jobID] = struct{}{}
		}
	}
	jobIDs := make([]string, 0, len(seen))
	for jobID := range seen {
		jobIDs = append(jobIDs, jobID)
	}
	slices.Sort(jobIDs)
	return jobIDs
}

func deletePodAndAssertCleanup(
	ctx context.Context,
	t *testing.T,
	config *envconf.Config,
	crClient client.Client,
	pod *corev1.Pod,
) {
	t.Helper()
	current := &corev1.Pod{}
	if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
		if !apierrors.IsNotFound(err) {
			t.Errorf("get pod %s/%s before cleanup: %v", pod.Namespace, pod.Name, err)
		}
		return
	}
	jobID := current.Labels[slurmJobIDLabel]
	claimName := ""
	if current.Status.ExtendedResourceClaimStatus != nil {
		claimName = current.Status.ExtendedResourceClaimStatus.ResourceClaimName
	}
	if jobID != "" && current.Status.Phase != corev1.PodSucceeded && current.Status.Phase != corev1.PodFailed &&
		!slices.Contains(current.Finalizers, wellknown.FinalizerScheduler) {
		t.Errorf("scheduled pod %s/%s is missing finalizer %q",
			current.Namespace, current.Name, wellknown.FinalizerScheduler)
	}
	if claimName != "" {
		claim := &resourcev1.ResourceClaim{}
		if err := crClient.Get(ctx, client.ObjectKey{Namespace: current.Namespace, Name: claimName}, claim); err != nil {
			t.Errorf("get generated ResourceClaim %s/%s before cleanup: %v", current.Namespace, claimName, err)
		}
	}
	if err := crClient.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		t.Errorf("delete pod %s/%s: %v", current.Namespace, current.Name, err)
		return
	}
	if err := wait.For(func(ctx context.Context) (bool, error) {
		err := crClient.Get(ctx, client.ObjectKeyFromObject(current), &corev1.Pod{})
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
		t.Errorf("pod %s/%s was not deleted after its finalizer cleanup: %v", current.Namespace, current.Name, err)
	}
	if err := waitForSlurmJobGone(ctx, config, crClient, jobID); err != nil {
		t.Errorf("Slurm job %s remained active after pod deletion: %v", jobID, err)
	}
	if claimName != "" {
		if err := wait.For(func(ctx context.Context) (bool, error) {
			err := crClient.Get(ctx, client.ObjectKey{Namespace: current.Namespace, Name: claimName}, &resourcev1.ResourceClaim{})
			return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
		}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
			t.Errorf("generated ResourceClaim %s/%s was not deleted: %v", current.Namespace, claimName, err)
		}
	}
}

func deleteObject(t *testing.T, ctx context.Context, crClient client.Client, object client.Object) {
	t.Helper()
	if err := crClient.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
		t.Errorf("delete %T %s/%s: %v", object, object.GetNamespace(), object.GetName(), err)
	}
}

func deletePodsAndAssertCleanup(
	ctx context.Context,
	t *testing.T,
	config *envconf.Config,
	crClient client.Client,
	pods ...*corev1.Pod,
) {
	t.Helper()
	jobIDs := map[string]struct{}{}
	claimNames := map[client.ObjectKey]struct{}{}
	for _, pod := range pods {
		current := &corev1.Pod{}
		if err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
			if !apierrors.IsNotFound(err) {
				t.Errorf("get pod %s/%s before cleanup: %v", pod.Namespace, pod.Name, err)
			}
			continue
		}
		if jobID := current.Labels[slurmJobIDLabel]; jobID != "" {
			jobIDs[jobID] = struct{}{}
			if current.Status.Phase != corev1.PodSucceeded && current.Status.Phase != corev1.PodFailed &&
				!slices.Contains(current.Finalizers, wellknown.FinalizerScheduler) {
				t.Errorf("scheduled pod %s/%s is missing finalizer %q",
					current.Namespace, current.Name, wellknown.FinalizerScheduler)
			}
		}
		if status := current.Status.ExtendedResourceClaimStatus; status != nil && status.ResourceClaimName != "" {
			claimNames[client.ObjectKey{Namespace: current.Namespace, Name: status.ResourceClaimName}] = struct{}{}
		}
		deleteObject(t, ctx, crClient, current)
	}
	for _, pod := range pods {
		if err := wait.For(func(ctx context.Context) (bool, error) {
			err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})
			return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
		}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
			t.Errorf("pod %s/%s was not deleted after finalizer cleanup: %v", pod.Namespace, pod.Name, err)
		}
	}
	for jobID := range jobIDs {
		if err := waitForSlurmJobGone(ctx, config, crClient, jobID); err != nil {
			t.Errorf("Slurm job %s remained active after pod deletion: %v", jobID, err)
		}
	}
	for key := range claimNames {
		if err := wait.For(func(ctx context.Context) (bool, error) {
			err := crClient.Get(ctx, key, &resourcev1.ResourceClaim{})
			return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
		}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
			t.Errorf("generated ResourceClaim %s was not deleted: %v", key, err)
		}
	}
}

func captureReleaseSignalDiagnostics(t *testing.T, featureName string, namespaces ...string) {
	t.Helper()
	if t.Failed() {
		captureFailureDiagnostics(t, featureName, namespaces...)
	}
}

func testAdmissionRoutingBoundaries() types.Feature {
	namespaceName := envconf.RandomName("e2e-routing", 40)
	managedPod := slurmTestPod(slurmBridgeNamespace,
		envconf.RandomName("admission-managed", 40), []string{"sh", "-c", "sleep 300"})
	managedPod.Spec.SchedulerName = corev1.DefaultSchedulerName
	explicitPod := slurmTestPod(namespaceName,
		envconf.RandomName("admission-explicit", 40), []string{"sh", "-c", "sleep 300"})
	controlPod := slurmTestPod(namespaceName,
		envconf.RandomName("admission-control", 40), []string{"sh", "-c", "sleep 300"})
	controlPod.Spec.SchedulerName = corev1.DefaultSchedulerName

	return features.New("Admission routing boundaries").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: namespaceName},
			}); err != nil {
				t.Fatalf("create unmanaged namespace: %v", err)
			}
			for _, pod := range []*corev1.Pod{managedPod, explicitPod, controlPod} {
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create pod %s/%s: %v", pod.Namespace, pod.Name, err)
				}
			}
			return ctx
		}).
		Assess("managed and explicit pods use Slurm while the control pod does not", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			for _, pod := range []*corev1.Pod{managedPod, explicitPod} {
				observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), podHasSlurmAllocation)
				if err != nil {
					t.Fatalf("pod %s/%s was not routed through Slurm: %v", pod.Namespace, pod.Name, err)
				}
				assertBridgePod(t, ctx, crClient, observed)
			}
			observedControl, err := waitForPod(ctx, crClient,
				client.ObjectKeyFromObject(controlPod),
				func(pod *corev1.Pod) bool { return pod.Status.Phase == corev1.PodRunning })
			if err != nil {
				t.Fatalf("control pod did not run with the default scheduler: %v", err)
			}
			controlPod = observedControl
			if controlPod.Spec.SchedulerName != corev1.DefaultSchedulerName {
				t.Errorf("control pod scheduler is %q, want %q",
					controlPod.Spec.SchedulerName, corev1.DefaultSchedulerName)
			}
			if controlPod.Labels[slurmJobIDLabel] != "" {
				t.Errorf("control pod unexpectedly has Slurm job ID %s", controlPod.Labels[slurmJobIDLabel])
			}
			node := &corev1.Node{}
			if err := crClient.Get(ctx, client.ObjectKey{Name: controlPod.Spec.NodeName}, node); err != nil {
				t.Fatalf("get control pod node: %v", err)
			}
			if node.Labels[slurmBridgeWorkerLabel] == "worker" {
				t.Errorf("default-scheduled control pod ran on managed bridge node %s", node.Name)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "admission routing boundaries",
				slurmBridgeNamespace, namespaceName, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, managedPod)
			deletePodAndAssertCleanup(ctx, t, config, crClient, explicitPod)
			deleteObject(t, ctx, crClient, controlPod)
			deleteObject(t, ctx, crClient, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			return ctx
		}).
		Feature()
}

func testSlurmBridgeParallelJobScheduling() types.Feature {
	jobName := envconf.RandomName("job-parallel-e2e", 40)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: slurmBridgeNamespace},
		Spec: batchv1.JobSpec{
			Parallelism:  ptr.To[int32](3),
			Completions:  ptr.To[int32](3),
			BackoffLimit: ptr.To[int32](0),
			Template:     slurmTestPodTemplate([]string{"sh", "-c", "sleep 5"}),
		},
	}

	return features.New("Parallel Kubernetes Job").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, job); err != nil {
				t.Fatalf("create parallel Job: %v", err)
			}
			return ctx
		}).
		Assess("all parallel pods receive independent Slurm allocations", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 3, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("parallel Job pods were not allocated: %v", err)
			}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
			}
			if jobIDs := podSlurmJobIDs(pods); len(jobIDs) != 3 {
				t.Errorf("parallel Job has %d distinct Slurm jobs, want 3: %v", len(jobIDs), jobIDs)
			}
			return ctx
		}).
		Assess("parallel Job completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 3, podFinished); err != nil {
				t.Fatalf("parallel Job did not complete: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "parallel Kubernetes Job",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deleteObject(t, ctx, crClient, job)
			return ctx
		}).
		Feature()
}

func testSlurmBridgeJobSetScheduling() types.Feature {
	jobSetName := envconf.RandomName("jobset-e2e", 40)
	jobSet := &jobsetv1alpha2.JobSet{
		ObjectMeta: metav1.ObjectMeta{Name: jobSetName, Namespace: slurmBridgeNamespace},
		Spec: jobsetv1alpha2.JobSetSpec{ReplicatedJobs: []jobsetv1alpha2.ReplicatedJob{{
			Name:     "workers",
			Replicas: 2,
			Template: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				Parallelism:  ptr.To[int32](1),
				Completions:  ptr.To[int32](1),
				BackoffLimit: ptr.To[int32](0),
				Template:     slurmTestPodTemplate([]string{"sh", "-c", "sleep 5"}),
			}},
		}}},
	}

	return features.New("JobSet workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, jobSet); err != nil {
				t.Fatalf("create JobSet: %v", err)
			}
			return ctx
		}).
		Assess("replicated jobs run through Slurm", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{jobsetv1alpha2.JobSetNameKey: jobSetName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("JobSet pods were not allocated: %v", err)
			}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
			}
			if jobIDs := podSlurmJobIDs(pods); len(jobIDs) != 2 {
				t.Errorf("JobSet has %d distinct Slurm jobs, want 2: %v", len(jobIDs), jobIDs)
			}
			return ctx
		}).
		Assess("JobSet completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{jobsetv1alpha2.JobSetNameKey: jobSetName}, 2, podFinished); err != nil {
				t.Fatalf("JobSet did not complete: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "JobSet workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "jobset-system")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deleteObject(t, ctx, crClient, jobSet)
			return ctx
		}).
		Feature()
}

func testKubernetesPodGroupScheduling() types.Feature {
	workloadName := envconf.RandomName("workload-e2e", 40)
	jobName := envconf.RandomName("podgroup-job-e2e", 40)
	podGroupName := jobName + "-workers"
	policy := schedulingv1alpha2.PodGroupSchedulingPolicy{
		Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 2},
	}
	workload := &schedulingv1alpha2.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: slurmBridgeNamespace},
		Spec: schedulingv1alpha2.WorkloadSpec{
			ControllerRef: &schedulingv1alpha2.TypedLocalObjectReference{
				APIGroup: "batch", Kind: "Job", Name: jobName,
			},
			PodGroupTemplates: []schedulingv1alpha2.PodGroupTemplate{{
				Name: "workers", SchedulingPolicy: policy,
			}},
		},
	}
	podGroup := &schedulingv1alpha2.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: podGroupName, Namespace: slurmBridgeNamespace},
		Spec: schedulingv1alpha2.PodGroupSpec{
			PodGroupTemplateRef: &schedulingv1alpha2.PodGroupTemplateReference{
				Workload: &schedulingv1alpha2.WorkloadPodGroupTemplateReference{
					WorkloadName: workloadName, PodGroupTemplateName: "workers",
				},
			},
			SchedulingPolicy: policy,
		},
	}
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

	return features.New("Kubernetes 1.36 PodGroup workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
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
			jobIDs := podSlurmJobIDs(pods)
			if len(jobIDs) != 1 {
				t.Fatalf("PodGroup pods have %d Slurm jobs, want 1: %v", len(jobIDs), jobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, jobIDs[0], 2)
			nodes := map[string]struct{}{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				nodes[pods[i].Spec.NodeName] = struct{}{}
			}
			if len(nodes) != 2 {
				t.Errorf("PodGroup pods use %d nodes, want 2: %v", len(nodes), nodes)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				observed := &schedulingv1alpha2.PodGroup{}
				if err := crClient.Get(ctx, client.ObjectKeyFromObject(podGroup), observed); err != nil {
					return false, err
				}
				for _, condition := range observed.Status.Conditions {
					if condition.Type == schedulingv1alpha2.PodGroupScheduled {
						return condition.Status == metav1.ConditionTrue, nil
					}
				}
				return false, nil
			}, wait.WithContext(ctx), wait.WithTimeout(slurmWorkloadTimeout), wait.WithInterval(3*time.Second)); err != nil {
				t.Errorf("PodGroup never reported scheduled: %v", err)
			}
			return ctx
		}).
		Assess("gang Job completes", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if _, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{batchv1.JobNameLabel: jobName}, 2, podFinished); err != nil {
				t.Fatalf("PodGroup Job did not complete: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Kubernetes 1.36 PodGroup workload",
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
			jobIDs := podSlurmJobIDs(observed)
			if len(jobIDs) != 1 {
				t.Fatalf("PodGroup pods have %d Slurm jobs, want 1: %v", len(jobIDs), jobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, jobIDs[0], 2)
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
				map[string]string{schedv1alpha1.PodGroupLabel: podGroupName}, 2, podFinished); err != nil {
				t.Fatalf("scheduler-plugins PodGroup pods did not complete: %v", err)
			}
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

func testLeaderWorkerSetScheduling() types.Feature {
	lwsName := envconf.RandomName("lws-e2e", 40)
	leaderTemplate := slurmTestPodTemplate([]string{"sh", "-c", "sleep 300"})
	workerTemplate := slurmTestPodTemplate([]string{"sh", "-c", "sleep 300"})
	leaderTemplate.Spec.RestartPolicy = corev1.RestartPolicyAlways
	workerTemplate.Spec.RestartPolicy = corev1.RestartPolicyAlways
	lws := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Name: lwsName, Namespace: slurmBridgeNamespace},
		Spec: lwsv1.LeaderWorkerSetSpec{
			Replicas:      ptr.To[int32](1),
			StartupPolicy: lwsv1.LeaderCreatedStartupPolicy,
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size:           ptr.To[int32](2),
				RestartPolicy:  lwsv1.RecreateGroupOnPodRestart,
				LeaderTemplate: &leaderTemplate,
				WorkerTemplate: workerTemplate,
			},
		},
	}

	return features.New("LeaderWorkerSet workload").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, lws); err != nil {
				t.Fatalf("create LeaderWorkerSet: %v", err)
			}
			return ctx
		}).
		Assess("leader and worker share one two-node Slurm job", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			pods, err := waitForLabeledPods(ctx, crClient, slurmBridgeNamespace,
				map[string]string{lwsv1.SetNameLabelKey: lwsName}, 2, podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("LeaderWorkerSet pods were not allocated: %v", err)
			}
			jobIDs := podSlurmJobIDs(pods)
			if len(jobIDs) != 1 {
				t.Fatalf("LeaderWorkerSet pods have %d Slurm jobs, want 1: %v", len(jobIDs), jobIDs)
			}
			assertSlurmNodeCount(ctx, t, config, crClient, jobIDs[0], 2)
			nodes := map[string]struct{}{}
			for i := range pods {
				assertBridgePod(t, ctx, crClient, &pods[i])
				nodes[pods[i].Spec.NodeName] = struct{}{}
			}
			if len(nodes) != 2 {
				t.Errorf("LeaderWorkerSet group uses %d nodes, want 2: %v", len(nodes), nodes)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "LeaderWorkerSet workload",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace, "lws-system")
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			podList := &corev1.PodList{}
			if err := crClient.List(ctx, podList, client.InNamespace(slurmBridgeNamespace),
				client.MatchingLabels{lwsv1.SetNameLabelKey: lwsName}); err != nil {
				t.Errorf("list LeaderWorkerSet pods for cleanup: %v", err)
			}
			deleteObject(t, ctx, crClient, lws)
			pods := make([]*corev1.Pod, 0, len(podList.Items))
			for i := range podList.Items {
				pods = append(pods, &podList.Items[i])
			}
			deletePodsAndAssertCleanup(ctx, t, config, crClient, pods...)
			return ctx
		}).
		Feature()
}

func testKubernetesPlacementConstraints() types.Feature {
	podName := envconf.RandomName("placement-e2e", 40)
	pod := slurmTestPod(slurmBridgeNamespace, podName, []string{"sh", "-c", "sleep 300"})
	pod.Spec.NodeSelector = map[string]string{
		corev1.LabelOSStable:   "linux",
		slurmBridgeWorkerLabel: "worker",
	}

	return features.New("Kubernetes placement constraints").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create placement-constrained pod: %v", err)
			}
			return ctx
		}).
		Assess("allocated node satisfies the Kubernetes node selector", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("placement-constrained pod was not allocated: %v", err)
			}
			pod = observed
			node := &corev1.Node{}
			if err := crClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
				t.Fatalf("get allocated node %s: %v", pod.Spec.NodeName, err)
			}
			for key, value := range pod.Spec.NodeSelector {
				if node.Labels[key] != value {
					t.Errorf("allocated node %s has %s=%q, want %q", node.Name, key, node.Labels[key], value)
				}
			}
			output, err := querySlurmJob(ctx, config, crClient, pod.Labels[slurmJobIDLabel])
			if err != nil {
				t.Fatal(err)
			}
			nodeList, err := slurmJobNodeList(output)
			if err != nil {
				t.Fatal(err)
			}
			if nodeList != pod.Spec.NodeName {
				t.Errorf("Slurm allocated %s, want Kubernetes-bound node %s", nodeList, pod.Spec.NodeName)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Kubernetes placement constraints",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			return ctx
		}).
		Feature()
}

func testSlurmJobRoundTrip() types.Feature {
	podName := envconf.RandomName("roundtrip-pod-e2e", 40)
	jobName := envconf.RandomName("roundtrip-job-e2e", 40)
	pod := slurmTestPod(slurmBridgeNamespace, podName, []string{"sh", "-c", "sleep 300"})
	pod.Annotations = map[string]string{
		wellknown.AnnotationJobName:   jobName,
		wellknown.AnnotationPartition: slurmBridgePartition,
		wellknown.AnnotationTimeLimit: "5",
		wellknown.AnnotationMinNodes:  "1",
		wellknown.AnnotationMaxNodes:  "1",
		wellknown.AnnotationExclusive: "true",
	}
	pod.Spec.Containers[0].Resources = slurmTestResources("2", "200Mi")

	return features.New("Slurm annotation and resource round trip").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create round-trip pod: %v", err)
			}
			return ctx
		}).
		Assess("Slurm job matches Kubernetes annotations and resources", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("round-trip pod was not allocated: %v", err)
			}
			pod = observed
			output, err := querySlurmJob(ctx, config, crClient, pod.Labels[slurmJobIDLabel])
			if err != nil {
				t.Fatal(err)
			}
			wantFields := map[string]string{
				"JobName":       jobName,
				"Partition":     slurmBridgePartition,
				"TimeLimit":     "00:05:00",
				"NumNodes":      "1",
				"CPUs/Task":     "2",
				"MinMemoryNode": "200M",
				"OverSubscribe": "NO",
			}
			for field, want := range wantFields {
				got, err := slurmJobField(output, field)
				if err != nil {
					t.Error(err)
					continue
				}
				if got != want {
					t.Errorf("Slurm %s=%q, want %q", field, got, want)
				}
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Slurm annotation and resource round trip",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			return ctx
		}).
		Feature()
}

func testKubernetesCancellation() types.Feature {
	podName := envconf.RandomName("cancel-kubernetes-e2e", 40)
	pod := slurmTestPod(slurmBridgeNamespace, podName, []string{"sh", "-c", "sleep 300"})
	deleted := false

	return features.New("Kubernetes to Slurm cancellation").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create cancellation pod: %v", err)
			}
			return ctx
		}).
		Assess("deleting the pod cancels and removes its Slurm job", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("cancellation pod was not allocated: %v", err)
			}
			pod = observed
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			deleted = true
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Kubernetes to Slurm cancellation",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if deleted || !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			return ctx
		}).
		Feature()
}

func testSlurmCancellation() types.Feature {
	podName := envconf.RandomName("cancel-slurm-e2e", 40)
	pod := slurmTestPod(slurmBridgeNamespace, podName, []string{"sh", "-c", "sleep 300"})
	deleted := false

	return features.New("Slurm to Kubernetes cancellation").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			if err := crClient.Create(ctx, pod); err != nil {
				t.Fatalf("create cancellation pod: %v", err)
			}
			return ctx
		}).
		Assess("canceling the Slurm job terminates the pod", func(
			ctx context.Context,
			t *testing.T,
			config *envconf.Config,
		) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatalf("get client: %v", err)
			}
			observed, err := waitForPod(ctx, crClient, client.ObjectKeyFromObject(pod), podHasSlurmAllocation)
			if err != nil {
				t.Fatalf("cancellation pod was not allocated: %v", err)
			}
			pod = observed
			jobID := pod.Labels[slurmJobIDLabel]
			controllerPod, err := getSlurmControllerPod(ctx, crClient)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execInPod(ctx, config, controllerPod, "scancel", jobID); err != nil {
				t.Fatalf("cancel Slurm job %s: %v", jobID, err)
			}
			if err := wait.For(func(ctx context.Context) (bool, error) {
				err := crClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})
				return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
			}, wait.WithContext(ctx), wait.WithTimeout(slurmCleanupTimeout), wait.WithInterval(2*time.Second)); err != nil {
				t.Fatalf("pod was not deleted after Slurm cancellation: %v", err)
			}
			deleted = true
			if err := waitForSlurmJobGone(ctx, config, crClient, jobID); err != nil {
				t.Errorf("Slurm job %s remained queued after cancellation: %v", jobID, err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "Slurm to Kubernetes cancellation",
				slurmBridgeNamespace, slurmNamespace, slinkyNamespace)
			if deleted || !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Errorf("get client for cleanup: %v", err)
				return ctx
			}
			deletePodAndAssertCleanup(ctx, t, config, crClient, pod)
			return ctx
		}).
		Feature()
}
