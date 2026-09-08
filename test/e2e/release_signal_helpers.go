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

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
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

func skipKubernetesPodGroupOnUnsupportedVersion(t *testing.T, config *envconf.Config) {
	t.Helper()
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config.Client().RESTConfig())
	if err != nil {
		t.Fatalf("create Kubernetes discovery client: %v", err)
	}
	serverInfo, err := discoveryClient.ServerVersion()
	if err != nil {
		t.Fatalf("discover Kubernetes server version: %v", err)
	}
	serverVersion, err := utilversion.ParseGeneric(serverInfo.GitVersion)
	if err != nil {
		t.Fatalf("parse Kubernetes server version %q: %v", serverInfo.GitVersion, err)
	}
	if serverVersion.Major() == 1 && serverVersion.Minor() == 35 {
		t.Skipf("Kubernetes %s serves the unsupported v1alpha1 PodGroup API", serverVersion)
	}
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

func podFinishedAndReleased(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded &&
		!slices.Contains(pod.Finalizers, wellknown.FinalizerScheduler)
}

func assertSlurmJobsGone(
	ctx context.Context,
	t *testing.T,
	config *envconf.Config,
	crClient client.Client,
	jobIDs []string,
) {
	t.Helper()
	for _, jobID := range jobIDs {
		if err := waitForSlurmJobGone(ctx, config, crClient, jobID); err != nil {
			t.Errorf("Slurm job %s remained active after workload completion: %v", jobID, err)
		}
	}
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

func assertKubernetesPodGroupScheduled(
	ctx context.Context,
	t *testing.T,
	crClient client.Client,
	podGroup *schedulingv1alpha2.PodGroup,
) {
	t.Helper()
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
		t.Errorf("PodGroup %s/%s never reported scheduled: %v",
			podGroup.Namespace, podGroup.Name, err)
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
		if jobID := pod.Labels[slurmJobIDLabel]; jobID != "" {
			jobIDs[jobID] = struct{}{}
		}
		if status := pod.Status.ExtendedResourceClaimStatus; status != nil && status.ResourceClaimName != "" {
			claimNames[client.ObjectKey{Namespace: pod.Namespace, Name: status.ResourceClaimName}] = struct{}{}
		}
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
