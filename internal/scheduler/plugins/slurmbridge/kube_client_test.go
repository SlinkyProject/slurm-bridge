// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestPodGroupCoschedulingCache(t *testing.T) {
	scheme, err := newClientScheme()
	if err != nil {
		t.Fatal(err)
	}
	pg := &sched.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pg1"},
		Spec:       sched.PodGroupSpec{MinMember: 48},
	}
	gets := 0
	transport := kubeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, fmt.Errorf("unexpected %s %s", req.Method, req.URL.Path)
		}
		gets++
		data, err := json.Marshal(pg)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {runtime.ContentTypeJSON}},
			Body:       io.NopCloser(bytes.NewReader(data)),
			Request:    req,
		}, nil
	})
	// discovery stubs so client.New can build its REST mapper
	discovery := map[string]any{
		"/api":                               &metav1.APIVersions{Versions: []string{"v1"}},
		"/apis":                              &metav1.APIGroupList{Groups: []metav1.APIGroup{{Name: "scheduling.x-k8s.io", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "scheduling.x-k8s.io/v1alpha1", Version: "v1alpha1"}}}}},
		"/apis/scheduling.x-k8s.io/v1alpha1": &metav1.APIResourceList{GroupVersion: "scheduling.x-k8s.io/v1alpha1", APIResources: []metav1.APIResource{{Name: "podgroups", Kind: "PodGroup", Namespaced: true}}},
	}
	config := &rest.Config{
		Host: "https://kubernetes.test",
		Transport: kubeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if d, ok := discovery[req.URL.Path]; ok {
				data, _ := json.Marshal(d)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {runtime.ContentTypeJSON}}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
			}
			return transport.RoundTrip(req)
		}),
		ContentConfig: rest.ContentConfig{ContentType: runtime.ContentTypeJSON},
	}
	kubeClient, err := newKubeClient(config, scheme)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := client.ObjectKeyFromObject(pg)
	// First Get: hits apiserver.
	var got1 sched.PodGroup
	if err := kubeClient.Get(ctx, key, &got1); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if got1.Spec.MinMember != 48 {
		t.Fatalf("first Get MinMember = %d, want 48", got1.Spec.MinMember)
	}
	// Second Get: must be served from cache, no HTTP call.
	var got2 sched.PodGroup
	if err := kubeClient.Get(ctx, key, &got2); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if gets != 1 {
		t.Fatalf("apiserver Get count = %d, want 1 (second should hit cache)", gets)
	}
	if got2.Spec.MinMember != 48 {
		t.Fatalf("cached Get MinMember = %d, want 48", got2.Spec.MinMember)
	}

	// Invalidating via Delete clears the cache so the next Get re-fetches.
	// This covers slurm-bridge's own mutation path (Delete/Update/Patch).
	if err := kubeClient.Delete(ctx, &got2); err == nil {
		t.Fatal("Delete on fake transport should error (no DELETE handler), want error")
	}
	var got3 sched.PodGroup
	if err := kubeClient.Get(ctx, key, &got3); err != nil {
		t.Fatalf("Get after Delete-invalidation: %v", err)
	}
	if gets != 2 {
		t.Fatalf("apiserver Get count after invalidation = %d, want 2 (cache was dropped)", gets)
	}

	// Storing a fresh entry sweeps expired entries for other PodGroups.
	stale := types.NamespacedName{Namespace: "ns", Name: "gone"}
	cache := &kubeClient.(*podGroupJSONClient).coschedulingCache
	cache.Store(stale, coschedulingCacheEntry{expires: time.Now().Add(-time.Second)})
	cache.Delete(types.NamespacedName(key))
	if err := kubeClient.Get(ctx, key, &got3); err != nil {
		t.Fatalf("Get for sweep: %v", err)
	}
	if _, ok := cache.Load(stale); !ok {
		return
	}
	t.Fatal("expired entry for another PodGroup was not swept")
}

// TestPodGroupCoschedulingCacheTerminalPhase verifies that objects fetched in a
// terminal phase are never cached, so each caller goes to the apiserver.
func TestPodGroupCoschedulingCacheTerminalPhase(t *testing.T) {
	scheme, err := newClientScheme()
	if err != nil {
		t.Fatal(err)
	}
	pg := &sched.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pg1"},
		Spec:       sched.PodGroupSpec{MinMember: 48},
		Status:     sched.PodGroupStatus{Phase: sched.PodGroupRunning},
	}
	gets := 0
	discovery := map[string]any{
		"/api":                               &metav1.APIVersions{Versions: []string{"v1"}},
		"/apis":                              &metav1.APIGroupList{Groups: []metav1.APIGroup{{Name: "scheduling.x-k8s.io", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "scheduling.x-k8s.io/v1alpha1", Version: "v1alpha1"}}}}},
		"/apis/scheduling.x-k8s.io/v1alpha1": &metav1.APIResourceList{GroupVersion: "scheduling.x-k8s.io/v1alpha1", APIResources: []metav1.APIResource{{Name: "podgroups", Kind: "PodGroup", Namespaced: true}}},
	}
	config := &rest.Config{
		Host: "https://kubernetes.test",
		Transport: kubeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if d, ok := discovery[req.URL.Path]; ok {
				data, _ := json.Marshal(d)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {runtime.ContentTypeJSON}}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
			}
			gets++
			data, _ := json.Marshal(pg)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {runtime.ContentTypeJSON}}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
		}),
		ContentConfig: rest.ContentConfig{ContentType: runtime.ContentTypeJSON},
	}
	kubeClient, err := newKubeClient(config, scheme)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := client.ObjectKeyFromObject(pg)
	var got1, got2 sched.PodGroup
	if err := kubeClient.Get(ctx, key, &got1); err != nil {
		t.Fatalf("first terminal-phase Get: %v", err)
	}
	if err := kubeClient.Get(ctx, key, &got2); err != nil {
		t.Fatalf("second terminal-phase Get: %v", err)
	}
	if gets != 2 {
		t.Fatalf("apiserver Get count = %d, want 2 (terminal phase must never be cached)", gets)
	}
}

type kubeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f kubeRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestKubeClientContentNegotiation(t *testing.T) {
	for _, version := range []string{"", slurmjobir.WorkloadAPIVersionV1Alpha2, slurmjobir.WorkloadAPIVersionV1Beta1} {
		name := version
		if name == "" {
			name = "without-workload-api"
		}
		t.Run(name, func(t *testing.T) {
			scheme, err := newClientScheme()
			if err != nil {
				t.Fatal(err)
			}
			var workloadAPI *slurmjobir.WorkloadAPI
			if version != "" {
				workloadAPI = mustRegisterTestWorkloadAPI(t, scheme, version)
			}
			const namespace = "default"
			pod := &corev1.Pod{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace, Name: "worker",
					Labels:      map[string]string{wellknown.LabelExternalJobId: "5"},
					Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node-a"},
				},
				Spec: corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}},
			}
			groupVersion := "scheduling.k8s.io/" + version
			pg := &slurmjobir.PodGroup{
				TypeMeta:   metav1.TypeMeta{APIVersion: groupVersion, Kind: "PodGroup"},
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "group", Generation: 1},
				Spec: slurmjobir.PodGroupSpec{
					SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
						Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1},
					},
				},
			}
			if version == slurmjobir.WorkloadAPIVersionV1Alpha2 {
				pg.Spec.PodGroupTemplateRef = &slurmjobir.PodGroupTemplateReference{
					Workload: &slurmjobir.WorkloadPodGroupTemplateReference{WorkloadName: "workload"},
				}
			} else {
				pg.Spec.WorkloadRef = &slurmjobir.WorkloadReference{WorkloadName: "workload"}
			}
			pgPath := "/apis/" + groupVersion + "/namespaces/" + namespace + "/podgroups/group"
			pgListPath := "/apis/" + groupVersion + "/namespaces/" + namespace + "/podgroups"
			workloadPath := "/apis/" + groupVersion + "/namespaces/" + namespace + "/workloads/workload"
			metadataType := metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"}
			objects := map[string]runtime.Object{
				"/api/v1/namespaces/default/pods/worker": pod,
				"/api/v1/namespaces/default/pods": &corev1.PodList{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{*pod},
				},
				"/api/v1/nodes": &corev1.NodeList{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"},
					Items:    []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}},
				},
				workloadPath: &metav1.PartialObjectMetadata{
					TypeMeta: metadataType,
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace, Name: "workload",
						Annotations: map[string]string{wellknown.AnnotationQOS: "workload-qos"},
					},
				},
			}
			discovery := map[string]any{
				"/api":  &metav1.APIVersions{Versions: []string{"v1"}},
				"/apis": &metav1.APIGroupList{},
				"/api/v1": &metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
					{Name: "pods", Kind: "Pod", Namespaced: true},
					{Name: "nodes", Kind: "Node"},
				}},
				"/apis/" + groupVersion: &metav1.APIResourceList{GroupVersion: groupVersion, APIResources: []metav1.APIResource{
					{Name: "podgroups", Kind: "PodGroup", Namespaced: true},
					{Name: "workloads", Kind: "Workload", Namespaced: true},
				}},
			}
			protobuf, ok := runtime.SerializerInfoForMediaType(serializer.NewCodecFactory(scheme).SupportedMediaTypes(), runtime.ContentTypeProtobuf)
			if !ok {
				t.Fatal("protobuf serializer unavailable")
			}
			podGroupGets, podGroupLists, statusPatches, workloadGets, podStatusPatches := 0, 0, 0, 0, 0
			transport := kubeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				var data []byte
				var err error
				contentType := runtime.ContentTypeJSON
				accept := req.Header.Get("Accept")
				if discoveryObject, ok := discovery[req.URL.Path]; ok {
					data, err = json.Marshal(discoveryObject)
				} else if (req.URL.Path == pgPath || req.URL.Path == pgPath+"/status" || req.URL.Path == pgListPath) && !strings.Contains(accept, "as=PartialObjectMetadata") {
					if accept != runtime.ContentTypeJSON {
						t.Errorf("PodGroup %s Accept = %q, want JSON only", req.Method, accept)
					}
					if req.URL.Path == pgListPath {
						podGroupLists++
						data, err = json.Marshal(&slurmjobir.PodGroupList{
							TypeMeta: metav1.TypeMeta{APIVersion: groupVersion, Kind: "PodGroupList"},
							Items:    []slurmjobir.PodGroup{*pg},
						})
					} else {
						data, err = json.Marshal(pg)
					}
					if req.Method == http.MethodPatch && err == nil {
						statusPatches++
						if req.URL.Path != pgPath+"/status" || req.Header.Get("Content-Type") != string(client.StrategicMergeFrom(pg).Type()) {
							t.Errorf("unexpected PodGroup patch: %s, Content-Type %q", req.URL.Path, req.Header.Get("Content-Type"))
						}
						patch, readErr := io.ReadAll(req.Body)
						if readErr != nil {
							return nil, readErr
						}
						data, err = strategicpatch.StrategicMergePatch(data, patch, slurmjobir.PodGroup{})
						if err == nil {
							err = json.Unmarshal(data, pg)
						}
					} else if req.URL.Path != pgListPath && req.Method == http.MethodGet {
						podGroupGets++
					}
				} else {
					if !strings.HasPrefix(accept, runtime.ContentTypeProtobuf) {
						t.Errorf("%s Accept = %q, want protobuf preferred", req.URL.Path, accept)
					}
					obj := objects[req.URL.Path]
					if req.URL.Path == "/api/v1/namespaces/default/pods/worker/status" && req.Method == http.MethodPatch {
						podStatusPatches++
						patch, readErr := io.ReadAll(req.Body)
						if readErr != nil {
							return nil, readErr
						}
						if err := json.Unmarshal(patch, pod); err != nil {
							return nil, err
						}
						obj = pod
					}
					if req.URL.Path == pgPath {
						obj = &metav1.PartialObjectMetadata{TypeMeta: metadataType, ObjectMeta: pg.ObjectMeta}
					}
					if obj == nil {
						return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
					}
					if req.URL.Path == workloadPath {
						workloadGets++
					}
					contentType = runtime.ContentTypeProtobuf
					data, err = runtime.Encode(protobuf.Serializer, obj)
				}
				if err != nil {
					return nil, err
				}
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}},
					Body: io.NopCloser(bytes.NewReader(data)), Request: req,
				}, nil
			})
			config := &rest.Config{
				Host: "https://kubernetes.test", Transport: transport,
				ContentConfig: rest.ContentConfig{ContentType: runtime.ContentTypeProtobuf},
			}
			originalContentConfig := config.ContentConfig
			kubeClient, err := newKubeClient(config, scheme)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(config.ContentConfig, originalContentConfig) {
				t.Fatal("client construction changed the scheduler content configuration")
			}
			if kubeClient.RESTMapper() != kubeClient.(*podGroupJSONClient).jsonClient.RESTMapper() {
				t.Fatal("clients do not share their REST mapper")
			}
			ctx := context.Background()
			var fetchedPod corev1.Pod
			if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(pod), &fetchedPod); err != nil {
				t.Fatalf("Get Pod: %v", err)
			}
			var nodes corev1.NodeList
			if err := kubeClient.List(ctx, &nodes); err != nil {
				t.Fatalf("List Nodes: %v", err)
			}
			if fetchedPod.Name != pod.Name || len(nodes.Items) != 1 || nodes.Items[0].Name != "node-a" {
				t.Fatal("protobuf responses were not decoded correctly")
			}
			originalPod := fetchedPod.DeepCopy()
			fetchedPod.Status.Phase = corev1.PodRunning
			if err := kubeClient.Status().Patch(ctx, &fetchedPod, client.MergeFrom(originalPod)); err != nil {
				t.Fatalf("Patch Pod status: %v", err)
			}
			if podStatusPatches != 1 || pod.Status.Phase != corev1.PodRunning || fetchedPod.Status.Phase != corev1.PodRunning {
				t.Fatal("Pod status patch did not round-trip through protobuf")
			}
			if workloadAPI == nil {
				return
			}
			var podGroups slurmjobir.PodGroupList
			if err := kubeClient.List(ctx, &podGroups, client.InNamespace(namespace)); err != nil {
				t.Fatalf("List PodGroups: %v", err)
			}
			if len(podGroups.Items) != 1 || podGroups.Items[0].Name != pg.Name {
				t.Fatalf("List PodGroups = %#v, want PodGroup %q", podGroups.Items, pg.Name)
			}
			sb := &SlurmBridge{Client: kubeClient, workloadAPI: workloadAPI, schedulerName: "slurm-bridge"}
			ir, err := slurmjobir.TranslateToSlurmJobIR(sb.Client, dra.DefaultRegistry(), workloadAPI, ctx, pod)
			if err != nil {
				t.Fatalf("TranslateToSlurmJobIR: %v", err)
			}
			componentIndex := ir.ComponentOf(pod.Namespace, pod.Name)
			if componentIndex < 0 {
				t.Fatalf("ComponentOf(%q, %q) = %d, want translated pod component", pod.Namespace, pod.Name, componentIndex)
			}
			component := &ir.Components[componentIndex]
			if len(component.Pods.Items) != 1 || ptr.Deref(component.JobInfo.QOS, "") != "workload-qos" {
				t.Fatalf("unexpected translation: %#v", ir)
			}
			if status := slurmjobir.PreFilter(sb.Client, dra.DefaultRegistry(), workloadAPI, ctx, pod, ir); !status.IsSuccess() {
				t.Fatalf("PreFilter: %v", status)
			}
			sb.markPodGroupScheduled(ctx, ir, component, "5")
			if condition := apimeta.FindStatusCondition(pg.Status.Conditions, workloadAPI.ScheduledCondition); condition == nil || condition.Status != metav1.ConditionTrue {
				t.Fatalf("scheduled condition = %#v, want True", condition)
			}
			if podGroupGets != 3 || podGroupLists != 1 || statusPatches != 1 || workloadGets != 1 {
				t.Fatalf("requests: PodGroup GETs=%d, PodGroup LISTs=%d, status PATCHes=%d, Workload metadata GETs=%d; want 3, 1, 1, 1", podGroupGets, podGroupLists, statusPatches, workloadGets)
			}
		})
	}
}
