// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package workloadclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	scheduling "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	apply "k8s.io/client-go/applyconfigurations/scheduling/v1beta1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerutil "k8s.io/kubernetes/pkg/scheduler/util"
)

func TestSchedulerPodGroupClient(t *testing.T) {
	for _, version := range []string{"v1alpha2", "v1beta1"} {
		t.Run(version, func(t *testing.T) {
			condition := alphaPodGroupScheduled
			if version == "v1beta1" {
				condition = "PodGroupInitiallyScheduled"
			}
			group := map[string]any{
				"apiVersion": "scheduling.k8s.io/" + version, "kind": "PodGroup",
				"metadata": map[string]any{"name": "gang", "namespace": "test", "resourceVersion": "12", "uid": "group-uid"},
				"spec":     map[string]any{"schedulingPolicy": map[string]any{"gang": map[string]any{"minCount": 48}}},
				"status":   map[string]any{"conditions": []any{map[string]any{"type": condition, "status": "True", "reason": "Scheduled", "lastTransitionTime": "2026-09-17T20:00:00Z"}}},
			}
			if version == "v1beta1" {
				group["spec"].(map[string]any)["disruptionMode"] = map[string]any{"single": map[string]any{}}
			} else {
				group["spec"].(map[string]any)["disruptionMode"] = "Pod"
			}
			patches := make(chan map[string]any, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				encode := func(v any) { _ = json.NewEncoder(w).Encode(v) }
				prefix := "/apis/scheduling.k8s.io/" + version
				switch {
				case r.URL.Path == prefix:
					encode(metav1.APIResourceList{GroupVersion: "scheduling.k8s.io/" + version,
						APIResources: []metav1.APIResource{{Name: "podgroups"}, {Name: "workloads"}}})
				case r.URL.Path == "/api/v1/namespaces/test/pods" || r.URL.Path == "/api/v1/pods":
					if r.URL.Query().Get("watch") == "true" {
						encode(map[string]any{"type": "BOOKMARK", "object": map[string]any{
							"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{
								"resourceVersion": "12", "annotations": map[string]any{"k8s.io/initial-events-end": "true"}}}})
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					encode(map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{"resourceVersion": "12"}, "items": []any{}})
				case r.URL.Path == prefix+"/namespaces/test/podgroups/gang/status":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					patches <- body
					encode(group)
				case r.URL.Path == prefix+"/namespaces/test/podgroups/gang":
					encode(group)
				case strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, "/podgroups"):
					if strings.Contains(r.URL.Path, "/namespaces/") {
						if r.URL.Query().Get("watch") == "true" {
							if r.URL.Query().Get("resourceVersion") != "12" || r.URL.Query().Get("allowWatchBookmarks") != "true" {
								t.Errorf("watch options lost: %s", r.URL.RawQuery)
							}
						} else if r.URL.Query().Get("labelSelector") != "test=true" {
							t.Errorf("list selector lost: %s", r.URL.RawQuery)
						}
					}
					if r.URL.Query().Get("watch") == "true" {
						if !strings.Contains(r.URL.Path, "/namespaces/") {
							encode(map[string]any{"type": "ADDED", "object": group})
							encode(map[string]any{"type": "BOOKMARK", "object": map[string]any{
								"apiVersion": "scheduling.k8s.io/" + version, "kind": "PodGroup", "metadata": map[string]any{
									"resourceVersion": "12", "annotations": map[string]any{"k8s.io/initial-events-end": "true"}}}})
							w.(http.Flusher).Flush()
							<-r.Context().Done()
							return
						}
						for _, typ := range []string{"ADDED", "MODIFIED", "DELETED", "BOOKMARK"} {
							encode(map[string]any{"type": typ, "object": group})
						}
						encode(map[string]any{"type": "ERROR", "object": metav1.Status{
							TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
							Status:   metav1.StatusFailure, Code: 410, Reason: metav1.StatusReasonExpired}})
						return
					}
					metadata := map[string]any{"resourceVersion": "12"}
					if strings.Contains(r.URL.Path, "/namespaces/") {
						metadata["continue"] = "next"
						metadata["remainingItemCount"] = 2
					}
					encode(map[string]any{"apiVersion": "scheduling.k8s.io/" + version, "kind": "PodGroupList",
						"metadata": metadata, "items": []any{group}})
				default:
					w.WriteHeader(http.StatusNotFound)
					encode(metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: 404})
				}
			}))
			defer server.Close()
			cfg := &rest.Config{Host: server.URL, QPS: 1000, Burst: 1000}
			base, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			client, err := New(cfg, base)
			if err != nil {
				t.Fatal(err)
			}
			if version == "v1beta1" && client != base {
				t.Fatal("beta cluster should retain its native typed client")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pgs := client.SchedulingV1beta1().PodGroups("test")
			list, err := pgs.List(ctx, metav1.ListOptions{LabelSelector: "test=true"})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 1 || list.ResourceVersion != "12" || list.Continue != "next" || list.RemainingItemCount == nil || *list.RemainingItemCount != 2 {
				t.Fatalf("list metadata/items lost: %#v", list)
			}
			pg, err := pgs.Get(ctx, "gang", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if pg.Spec.SchedulingPolicy.Gang.MinCount != 48 || pg.Status.Conditions[0].Type != scheduling.PodGroupInitiallyScheduled {
				t.Fatalf("incorrect converted group: %#v", pg)
			}
			if pg.Spec.DisruptionMode == nil || pg.Spec.DisruptionMode.Single == nil || pg.Spec.DisruptionMode.All != nil {
				t.Fatalf("incorrect converted disruption mode: %v", pg.Spec.DisruptionMode)
			}
			stream, err := pgs.Watch(ctx, metav1.ListOptions{ResourceVersion: "12", AllowWatchBookmarks: true})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Stop()
			for _, typ := range []watch.EventType{watch.Added, watch.Modified, watch.Deleted, watch.Bookmark, watch.Error} {
				select {
				case event := <-stream.ResultChan():
					if event.Type != typ {
						t.Fatalf("watch event = %s, want %s", event.Type, typ)
					}
					if typ == watch.Error {
						if status, ok := event.Object.(*metav1.Status); !ok || status.Code != 410 {
							t.Fatalf("lost watch error: %#v", event.Object)
						}
					} else if obj, ok := event.Object.(*scheduling.PodGroup); !ok || obj.Status.Conditions[0].Type != scheduling.PodGroupInitiallyScheduled {
						t.Fatalf("watch did not convert to scheduler type: %#v", event.Object)
					}
				case <-ctx.Done():
					t.Fatal("watch timed out")
				}
			}
			// Exercise the real scheduler status helper, including strategic patch
			// list ordering directives, rather than a handcrafted status request.
			if err := schedulerutil.PatchPodGroupStatus(ctx, client, "gang", "test", nil, &pg.Status); err != nil {
				t.Fatal(err)
			}
			body := <-patches
			conditions := body["status"].(map[string]any)["conditions"].([]any)
			if got := conditions[0].(map[string]any)["type"]; got != condition {
				t.Fatalf("wire condition = %v, want %s", got, condition)
			}
			if _, err := client.CoreV1().Pods("test").List(ctx, metav1.ListOptions{}); err != nil {
				t.Fatalf("ordinary Kubernetes client changed: %v", err)
			}
			// The scheduler's own informer must receive beta typed objects from
			// either API, without touching an unavailable resource URL.
			factory := scheduler.NewInformerFactory(client, 0, nil)
			informer := factory.Scheduling().V1beta1().PodGroups()
			_ = informer.Informer()
			factory.Start(ctx.Done())
			for _, synced := range factory.WaitForCacheSync(ctx.Done()) {
				if !synced {
					t.Fatal("scheduler PodGroup informer failed to sync")
				}
			}
			if cached, err := informer.Lister().PodGroups("test").Get("gang"); err != nil || cached.Spec.SchedulingPolicy.Gang.MinCount != 48 {
				t.Fatalf("scheduler informer did not cache the gang: %v, %v", cached, err)
			}
		})
	}
}

func TestAlphaSpecConversion(t *testing.T) {
	for _, mode := range []string{"Pod", "PodGroup", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
				"disruptionMode": mode,
				"podGroupTemplateRef": map[string]any{"workload": map[string]any{
					"workloadName": "training", "podGroupTemplateName": "workers",
				}},
			}}}
			pg, err := decode(obj, nil)
			if mode == "unknown" {
				if err == nil {
					t.Fatal("unknown disruption mode must not be silently dropped")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pg.Spec.DisruptionMode == nil || (pg.Spec.DisruptionMode.Single != nil) != (mode == "Pod") || (pg.Spec.DisruptionMode.All != nil) != (mode == "PodGroup") {
				t.Fatalf("incorrect alpha disruption mode conversion: %#v", pg.Spec)
			}
			if pg.Spec.WorkloadRef == nil || pg.Spec.WorkloadRef.WorkloadName != "training" || pg.Spec.WorkloadRef.TemplateName != "workers" {
				t.Fatalf("incorrect alpha workload reference conversion: %#v", pg.Spec)
			}
			if obj.Object["spec"].(map[string]any)["disruptionMode"] != mode {
				t.Fatal("conversion mutated its input")
			}
		})
	}
}

func TestDiscoveryDoesNotHideForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: 403})
	}))
	defer server.Close()
	cfg := &rest.Config{Host: server.URL}
	base, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(cfg, base)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("discovery error = %v, want Forbidden", err)
	}
}

func TestConditionPatchDirectives(t *testing.T) {
	var patch map[string]any
	data := fmt.Sprintf(`{"conditions":[{"type":%q,"$patch":"delete"},{"type":"Other","message":%q}],"$setElementOrder/conditions":[{"type":%q}]}`, scheduling.PodGroupInitiallyScheduled, scheduling.PodGroupInitiallyScheduled, scheduling.PodGroupInitiallyScheduled)
	if err := json.Unmarshal([]byte(data), &patch); err != nil {
		t.Fatal(err)
	}
	translateConditionTypes(patch, scheduling.PodGroupInitiallyScheduled, alphaPodGroupScheduled)
	conditions := patch["conditions"].([]any)
	if conditions[0].(map[string]any)["type"] != alphaPodGroupScheduled || conditions[1].(map[string]any)["message"] != scheduling.PodGroupInitiallyScheduled {
		t.Fatalf("incorrect condition translation: %#v", patch)
	}
	if patch["$setElementOrder/conditions"].([]any)[0].(map[string]any)["type"] != alphaPodGroupScheduled {
		t.Fatal("condition order key was not translated")
	}
}

func TestSpecWritesFailBeforeRequest(t *testing.T) {
	p := &podGroups{}
	ctx := context.Background()
	if _, err := p.Patch(ctx, "gang", types.MergePatchType, []byte(`{"spec":{}}`), metav1.PatchOptions{}); err == nil {
		t.Fatal("spec patch should be rejected")
	}
	if _, err := p.Update(ctx, &scheduling.PodGroup{}, metav1.UpdateOptions{}); err == nil {
		t.Fatal("spec replacement should be rejected")
	}
	if _, err := p.Create(ctx, &scheduling.PodGroup{}, metav1.CreateOptions{}); err == nil {
		t.Fatal("spec creation should be rejected")
	}
	if _, err := p.Apply(ctx, apply.PodGroup("gang", "test"), metav1.ApplyOptions{}); err == nil {
		t.Fatal("spec apply should be rejected")
	}
	if _, err := p.Patch(ctx, "gang", types.JSONPatchType, []byte(`[]`), metav1.PatchOptions{}, "status"); err == nil {
		t.Fatal("unsupported status patch type should be rejected")
	}
	if _, err := p.ApplyStatus(ctx, nil, metav1.ApplyOptions{}); err == nil {
		t.Fatal("nil status apply should be rejected")
	}
}

func TestAlphaConversionPreservesValidationFieldsAndMetadata(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"labels":      map[string]any{"type": alphaPodGroupScheduled},
			"annotations": map[string]any{"type": alphaPodGroupScheduled},
		},
		"spec": map[string]any{
			"schedulingConstraints": map[string]any{"topology": []any{map[string]any{"key": "topology.kubernetes.io/zone"}}},
			"resourceClaims":        []any{map[string]any{"name": "gpus", "resourceClaimName": "gpu-claim"}},
		},
	}}
	before := obj.DeepCopy()
	pg, err := decode(obj, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pg.Spec.SchedulingConstraints == nil || len(pg.Spec.SchedulingConstraints.Topology) != 1 || pg.Spec.SchedulingConstraints.Topology[0].Key != "topology.kubernetes.io/zone" {
		t.Fatalf("topology constraint lost: %#v", pg.Spec)
	}
	if len(pg.Spec.ResourceClaims) != 1 || pg.Spec.ResourceClaims[0].Name != "gpus" || pg.Spec.ResourceClaims[0].ResourceClaimName == nil || *pg.Spec.ResourceClaims[0].ResourceClaimName != "gpu-claim" {
		t.Fatalf("resource claims lost: %#v", pg.Spec)
	}
	if pg.Labels["type"] != alphaPodGroupScheduled || pg.Annotations["type"] != alphaPodGroupScheduled {
		t.Fatalf("metadata was translated as a condition: %#v", pg.ObjectMeta)
	}
	if !reflect.DeepEqual(before, obj) {
		t.Fatal("conversion mutated its input")
	}
}

func TestAlphaStatusWrites(t *testing.T) {
	for _, method := range []string{"UpdateStatus", "ApplyStatus"} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantMethod := http.MethodPatch
				if method == "UpdateStatus" {
					wantMethod = http.MethodPut
				}
				if r.Method != wantMethod || r.URL.Path != "/apis/scheduling.k8s.io/v1alpha2/namespaces/test/podgroups/gang/status" {
					t.Errorf("unexpected status request: %s %s", r.Method, r.URL.Path)
				}
				if r.URL.Query().Get("fieldManager") != "scheduler" || r.URL.Query().Get("dryRun") != "All" || (method == "UpdateStatus" && r.URL.Query().Get("fieldValidation") != "Strict") {
					t.Errorf("status write options lost: %s", r.URL.RawQuery)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, ok := body["spec"]; ok {
					t.Error("status write must not replace the alpha spec")
				}
				conditions := body["status"].(map[string]any)["conditions"].([]any)
				if got := conditions[0].(map[string]any)["type"]; got != alphaPodGroupScheduled {
					t.Errorf("wire condition = %v, want %s", got, alphaPodGroupScheduled)
				}
				if method == "ApplyStatus" {
					if body["apiVersion"] != "scheduling.k8s.io/v1alpha2" || r.Header.Get("Content-Type") != string(types.ApplyPatchType) || r.URL.Query().Get("force") != "true" {
						t.Errorf("incorrect status apply request: %#v, %s, %s", body, r.Header.Get("Content-Type"), r.URL.RawQuery)
					}
				} else if body["apiVersion"] != "scheduling.k8s.io/v1alpha2" || body["kind"] != "PodGroup" || body["metadata"].(map[string]any)["resourceVersion"] != "12" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("incorrect status update request: %#v, %s", body, r.Header.Get("Content-Type"))
				}
				body["apiVersion"], body["kind"] = "scheduling.k8s.io/v1alpha2", "PodGroup"
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			dyn, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			p := &podGroups{resource: dyn.Resource(scheduling.SchemeGroupVersion.WithResource("podgroups").GroupResource().WithVersion("v1alpha2")).Namespace("test")}
			group := &scheduling.PodGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "gang", Namespace: "test", ResourceVersion: "12"},
				Status:     scheduling.PodGroupStatus{Conditions: []metav1.Condition{{Type: scheduling.PodGroupInitiallyScheduled, Status: metav1.ConditionTrue, Reason: "Scheduled"}}},
			}
			var updated *scheduling.PodGroup
			if method == "UpdateStatus" {
				updated, err = p.UpdateStatus(t.Context(), group, metav1.UpdateOptions{DryRun: []string{"All"}, FieldManager: "scheduler", FieldValidation: "Strict"})
			} else {
				data, marshalErr := json.Marshal(group.Status)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				status := &apply.PodGroupStatusApplyConfiguration{}
				if err := json.Unmarshal(data, status); err != nil {
					t.Fatal(err)
				}
				updated, err = p.ApplyStatus(t.Context(), apply.PodGroup("gang", "test").WithStatus(status), metav1.ApplyOptions{DryRun: []string{"All"}, FieldManager: "scheduler", Force: true})
			}
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status.Conditions[0].Type != scheduling.PodGroupInitiallyScheduled || group.Status.Conditions[0].Type != scheduling.PodGroupInitiallyScheduled {
				t.Fatal("status write must preserve beta conditions in the result and caller's input")
			}
		})
	}
}

func TestAlphaUpdateStatusReplacesStatus(t *testing.T) {
	for _, name := range []string{"clear conditions", "clear claims", "clear all", "stale version"} {
		t.Run(name, func(t *testing.T) {
			stored := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "scheduling.k8s.io/v1alpha2", "kind": "PodGroup",
				"metadata": map[string]any{"name": "gang", "namespace": "test", "resourceVersion": "12", "labels": map[string]any{"keep": "label"}},
				"spec": map[string]any{
					"disruptionMode":      "PodGroup",
					"podGroupTemplateRef": map[string]any{"workload": map[string]any{"workloadName": "training", "podGroupTemplateName": "workers"}},
				},
				"status": map[string]any{
					"conditions":            []any{map[string]any{"type": alphaPodGroupScheduled, "status": "True", "reason": "Scheduled"}},
					"resourceClaimStatuses": []any{map[string]any{"name": "gpu", "resourceClaimName": "gpu-claim"}},
				},
			}}
			original := stored.DeepCopy()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodPut || r.URL.Path != "/apis/scheduling.k8s.io/v1alpha2/namespaces/test/podgroups/gang/status" {
					t.Errorf("unexpected status request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				var body unstructured.Unstructured
				if err := json.NewDecoder(r.Body).Decode(&body.Object); err != nil {
					t.Error(err)
					return
				}
				if body.GetResourceVersion() != stored.GetResourceVersion() {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonConflict, Code: http.StatusConflict})
					return
				}
				// The status subresource replaces status while preserving the stored
				// spec and metadata, including fields with alpha-only wire shapes.
				stored.Object["status"] = body.Object["status"]
				stored.SetResourceVersion("13")
				_ = json.NewEncoder(w).Encode(stored.Object)
			}))
			defer server.Close()
			dyn, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			p := &podGroups{resource: dyn.Resource(scheduling.SchemeGroupVersion.WithResource("podgroups").GroupResource().WithVersion("v1alpha2")).Namespace("test")}
			group, err := decode(original, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "clear conditions":
				group.Status.Conditions = nil
			case "clear claims":
				group.Status.ResourceClaimStatuses = nil
			case "clear all":
				group.Status = scheduling.PodGroupStatus{}
			case "stale version":
				group.ResourceVersion = "11"
			}
			before := group.DeepCopy()
			updated, err := p.UpdateStatus(t.Context(), group, metav1.UpdateOptions{})
			if !reflect.DeepEqual(group, before) {
				t.Fatal("status update mutated its input")
			}
			if name == "stale version" {
				if !apierrors.IsConflict(err) {
					t.Fatalf("update error = %v, want Conflict", err)
				}
				if !reflect.DeepEqual(stored, original) {
					t.Fatal("conflicting update changed the stored object")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(updated.Status, group.Status) {
				t.Fatalf("status = %#v, want %#v", updated.Status, group.Status)
			}
			if updated.ResourceVersion != "13" || !reflect.DeepEqual(updated.Spec, group.Spec) || !reflect.DeepEqual(updated.Labels, group.Labels) {
				t.Fatalf("status update lost resource version, spec, or labels: %#v", updated)
			}
		})
	}
}
