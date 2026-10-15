// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

// TestCompositePodGroupScheduling checks that gang children share one Slurm hetjob.
func TestCompositePodGroupScheduling(t *testing.T) {
	testCompositePodGroupScheduling(t, false)
}

// TestBasicCompositePodGroupScheduling checks that basic children run independently.
func TestBasicCompositePodGroupScheduling(t *testing.T) {
	testCompositePodGroupScheduling(t, true)
}

func testCompositePodGroupScheduling(t *testing.T, basic bool) {
	t.Helper()
	testEnv, err := newTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	name := envconf.RandomName("composite-e2e", 32)
	native := func(kind, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "scheduling.k8s.io/v1alpha3", "kind": kind,
			"metadata": map[string]any{"name": name, "namespace": slurmBridgeNamespace},
			"spec":     spec,
		}}
	}
	leafPolicy := map[string]any{"gang": map[string]any{"minCount": int64(1)}}
	rootPolicy := map[string]any{"gang": map[string]any{"minGroupCount": int64(2)}}
	if basic {
		rootPolicy = map[string]any{"basic": map[string]any{}}
	}
	workload := native("Workload", name, map[string]any{
		"compositePodGroupTemplates": []any{map[string]any{
			"name": "all", "schedulingPolicy": rootPolicy,
			"podGroupTemplates": []any{
				map[string]any{"name": "small", "schedulingPolicy": leafPolicy},
				map[string]any{"name": "large", "schedulingPolicy": leafPolicy},
			},
		}},
	})
	root := native("CompositePodGroup", name, map[string]any{
		"workloadRef":      map[string]any{"workloadName": name, "templateName": "all"},
		"schedulingPolicy": rootPolicy,
	})
	objects := []client.Object{workload, root}
	var pods []*corev1.Pod
	for i, leaf := range []string{"small", "large"} {
		groupName := name + "-" + leaf
		objects = append(objects, native("PodGroup", groupName, map[string]any{
			"parentCompositePodGroupName": name,
			"workloadRef":                 map[string]any{"workloadName": name, "templateName": leaf},
			"schedulingPolicy":            leafPolicy,
		}))
		pod := slurmTestPod(slurmBridgeNamespace, groupName, []string{"sh", "-c", "sleep 600"})
		pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: ptr.To(groupName)}
		pod.Spec.Containers[0].Resources = slurmTestResources(strconv.Itoa(i+1), testMemory)
		pods = append(pods, pod)
	}

	feature := features.New("CompositePodGroup scheduling").
		Setup(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			discoveryClient, err := discovery.NewDiscoveryClientForConfig(config.Client().RESTConfig())
			if err != nil {
				t.Fatalf("create Kubernetes discovery client: %v", err)
			}
			serverInfo, err := discoveryClient.ServerVersion()
			if err != nil {
				t.Fatalf("discover Kubernetes server version: %v", err)
			}
			serverVersion, err := utilversion.ParseSemantic(serverInfo.GitVersion)
			if err != nil {
				t.Fatalf("parse Kubernetes server version %q: %v", serverInfo.GitVersion, err)
			}
			if !serverVersion.AtLeast(utilversion.MustParseSemantic("v1.37.0")) { // codespell:ignore
				t.Skipf("CompositePodGroup requires Kubernetes >= v1.37.0; cluster runs %s", serverVersion)
			}

			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			// Publish the complete native hierarchy before creating its member Pods.
			for _, object := range objects {
				if err := crClient.Create(ctx, object); err != nil {
					t.Fatalf("create %s %s: %v", object.GetObjectKind().GroupVersionKind().Kind, object.GetName(), err)
				}
			}
			for i, pod := range pods {
				if basic && i > 0 {
					break
				}
				if err := crClient.Create(ctx, pod); err != nil {
					t.Fatalf("create Pod %s: %v", pod.Name, err)
				}
			}
			return ctx
		}).
		Assess("a basic child runs before its sibling exists", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			if !basic {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			allocationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			observed, err := waitForPod(allocationCtx, crClient, client.ObjectKeyFromObject(pods[0]), func(p *corev1.Pod) bool {
				return podHasSlurmAllocation(p) && p.Status.Phase == corev1.PodRunning
			})
			if err != nil {
				t.Fatalf("basic CompositePodGroup %s waited for its missing sibling: %v; status: %s", name, err, statusJSON(observed.Status))
			}
			pods[0] = observed
			if err := crClient.Create(ctx, pods[1]); err != nil {
				t.Fatalf("create sibling Pod %s: %v", pods[1].Name, err)
			}
			return ctx
		}).
		Assess("both leaf Pods run on Slurm Bridge workers", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			allocationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			for i, pod := range pods {
				observed, err := waitForPod(allocationCtx, crClient, client.ObjectKeyFromObject(pod), func(p *corev1.Pod) bool {
					return podHasSlurmAllocation(p) && p.Status.Phase == corev1.PodRunning
				})
				if err != nil {
					t.Fatalf("CompositePodGroup %s did not schedule Pod %s: %v; status: %s", name, pod.Name, err, statusJSON(observed.Status))
				}
				pods[i] = observed
				assertBridgePod(t, ctx, crClient, observed)
			}
			return ctx
		}).
		Assess("Slurm allocations match the composite scheduling policy", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			if basic {
				if pods[0].Labels[slurmJobIDLabel] == pods[1].Labels[slurmJobIDLabel] {
					t.Fatalf("basic children share Slurm job %s, want independent jobs", pods[0].Labels[slurmJobIDLabel])
				}
				for _, pod := range pods {
					output, err := querySlurmJob(ctx, config, crClient, pod.Labels[slurmJobIDLabel])
					if err != nil {
						t.Fatal(err)
					}
					if leader, err := slurmJobField(output, "HetJobId"); err == nil && leader != "0" {
						t.Errorf("basic child %s belongs to hetjob %s, want an independent job", pod.Name, leader)
					}
					for field, want := range map[string]string{"JobId": pod.Labels[slurmJobIDLabel], "NodeList": pod.Spec.NodeName} {
						if got, err := slurmJobField(output, field); err != nil || got != want {
							t.Errorf("Pod %s: Slurm %s=%q, want %q (error: %v)", pod.Name, field, got, want, err)
						}
					}
				}
				return ctx
			}
			// Inspect Slurm itself: two independent jobs must not satisfy this test.
			if pods[0].Labels[slurmJobIDLabel] == "" {
				t.Fatal("the first leaf Pod has no Slurm allocation")
			}
			output, err := querySlurmJob(ctx, config, crClient, pods[0].Labels[slurmJobIDLabel])
			if err != nil {
				t.Fatal(err)
			}
			leader, err := slurmJobField(output, "HetJobId")
			if err != nil {
				t.Fatalf("CompositePodGroup %s has no Slurm hetjob: %v", name, err)
			}
			if id, err := strconv.ParseUint(leader, 10, 32); err != nil || id == 0 {
				t.Fatalf("expected a positive Slurm HetJobId, got %q", leader)
			}
			output, err = querySlurmJob(ctx, config, crClient, leader)
			if err != nil {
				t.Fatal(err)
			}
			components := map[string]string{}
			for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
				jobID, err := slurmJobField(line, "JobId")
				if err != nil {
					t.Fatal(err)
				}
				components[jobID] = line
			}
			if len(components) != 2 {
				t.Fatalf("Slurm hetjob %s has %d components, want 2: %s", leader, len(components), output)
			}
			offsets := sets.New[string]()
			for _, pod := range pods {
				component, ok := components[pod.Labels[slurmJobIDLabel]]
				if !ok {
					t.Fatalf("Pod %s job %s is not in hetjob %s", pod.Name, pod.Labels[slurmJobIDLabel], leader)
				}
				for field, want := range map[string]string{"HetJobId": leader, "NodeList": pod.Spec.NodeName} {
					if got, err := slurmJobField(component, field); err != nil || got != want {
						t.Errorf("Pod %s: Slurm %s=%q, want %q (error: %v)", pod.Name, field, got, want, err)
					}
				}
				offset, err := slurmJobField(component, "HetJobOffset")
				if err != nil {
					t.Fatal(err)
				}
				offsets.Insert(offset)
			}
			if !offsets.Equal(sets.New("0", "1")) {
				t.Errorf("expected one Pod in each hetjob component, got offsets %v", sets.List(offsets))
			}
			return ctx
		}).
		Assess("live job lookups return the requested hetjob component", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			controller, err := getSlurmControllerPod(ctx, crClient)
			if err != nil {
				t.Fatal(err)
			}
			jwt, err := execInPod(ctx, config, controller, "scontrol", "token", "username=slurm", "lifespan=60")
			if err != nil {
				t.Fatal(err)
			}
			restConfig := config.Client().RESTConfig()
			httpClient, err := rest.HTTPClientFor(restConfig)
			if err != nil {
				t.Fatal(err)
			}
			slurmClient, err := slurmclient.NewClient(&slurmclient.Config{
				Server:        restConfig.Host + "/api/v1/namespaces/" + slurmNamespace + "/services/http:slurm-restapi:6820/proxy",
				HTTPClient:    httpClient,
				TokenProvider: token.StaticProvider(strings.TrimPrefix(strings.TrimSpace(jwt), "SLURM_JWT=")),
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, pod := range pods {
				jobID := pod.Labels[slurmJobIDLabel]
				job := &api.V0044JobInfo{}
				if err := slurmClient.Get(ctx, object.ObjectKey(jobID), job, &slurmclient.GetOptions{SkipCache: true}); err != nil {
					t.Fatal(err)
				}
				if got := strconv.Itoa(int(ptr.Deref(job.JobId, 0))); got != jobID {
					t.Errorf("live client Get(%s) returned job %s", jobID, got)
				}
				if got := ptr.Deref(job.Nodes, ""); got != pod.Spec.NodeName {
					t.Errorf("live client Get(%s) returned nodes %q, want %q", jobID, got, pod.Spec.NodeName)
				}
			}
			return ctx
		}).
		Assess("controller repairs Slurm metadata on running Pods", func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Fatal(err)
			}
			repairCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			for _, pod := range pods {
				offset := pod.Labels[wellknown.LabelExternalHetJobOffset]
				if !basic && offset == "" {
					t.Fatalf("Pod %s has no component offset", pod.Name)
				}
				damaged := pod.DeepCopy()
				damaged.Labels[wellknown.LabelExternalHetJobOffset] = "99"
				if err := crClient.Patch(repairCtx, damaged, client.MergeFrom(pod)); err != nil {
					t.Fatal(err)
				}
				repaired, err := waitForPod(repairCtx, crClient, client.ObjectKeyFromObject(pod), func(p *corev1.Pod) bool {
					return p.Labels[wellknown.LabelExternalHetJobOffset] == offset
				})
				if err != nil {
					t.Fatalf("Pod %s component offset was not repaired: %v", pod.Name, err)
				}
				if repaired.UID != pod.UID || repaired.Spec.NodeName != pod.Spec.NodeName || repaired.Labels[slurmJobIDLabel] != pod.Labels[slurmJobIDLabel] || repaired.Status.Phase != corev1.PodRunning {
					t.Fatalf("metadata repair changed Pod %s allocation or execution", pod.Name)
				}
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
			captureReleaseSignalDiagnostics(t, "CompositePodGroup scheduling", slurmBridgeNamespace, slurmNamespace)
			if !e2eCleanupEnabled(t) {
				return ctx
			}
			crClient, err := getControllerRuntimeClient(config)
			if err != nil {
				t.Error(err)
				return ctx
			}
			deletePodsAndAssertCleanup(ctx, t, config, crClient, pods...)
			for i := len(objects) - 1; i >= 0; i-- {
				deleteObject(t, ctx, crClient, objects[i])
			}
			return ctx
		}).Feature()
	_ = testEnv.Test(t, feature)
}
