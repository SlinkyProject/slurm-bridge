// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/tools/cache"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/component-base/metrics/legacyregistry"
	_ "k8s.io/component-base/metrics/prometheus/clientgo/fifo"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	"k8s.io/kubernetes/cmd/kube-scheduler/app/options"
	"k8s.io/kubernetes/pkg/features"
)

type clientProbe struct{}

func (*clientProbe) Name() string                                        { return "ClientProbe" }
func (*clientProbe) PreEnqueue(context.Context, *corev1.Pod) *fwk.Status { return nil }

func TestSetupPodGroupClient(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		version string
		status  int
		wantErr string
	}{
		{name: "disabled skips discovery"},
		{name: "alpha", enabled: true, version: "v1alpha2"},
		{name: "beta", enabled: true, version: "v1beta1"},
		{name: "missing API", enabled: true, status: http.StatusNotFound, wantErr: "neither"},
		{name: "forbidden", enabled: true, status: http.StatusForbidden, wantErr: "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, tc.enabled)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/apis/scheduling.k8s.io/") {
					requests.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				prefix := "/apis/scheduling.k8s.io/" + tc.version
				group := map[string]any{
					"apiVersion": "scheduling.k8s.io/" + tc.version, "kind": "PodGroup",
					"metadata": map[string]any{"name": "gang", "namespace": "test", "resourceVersion": "12"},
					"spec":     map[string]any{"schedulingPolicy": map[string]any{"gang": map[string]any{"minCount": 2}}},
				}
				switch {
				case tc.version != "" && r.URL.Path == prefix:
					_ = json.NewEncoder(w).Encode(metav1.APIResourceList{
						GroupVersion: "scheduling.k8s.io/" + tc.version,
						APIResources: []metav1.APIResource{{Name: "podgroups"}, {Name: "workloads"}},
					})
				case tc.version != "" && r.URL.Path == prefix+"/namespaces/test/podgroups/gang":
					_ = json.NewEncoder(w).Encode(group)
				case tc.version != "" && r.URL.Path == prefix+"/podgroups":
					if r.URL.Query().Get("watch") == "true" {
						_ = json.NewEncoder(w).Encode(map[string]any{"type": "ADDED", "object": group})
						_ = json.NewEncoder(w).Encode(map[string]any{"type": "BOOKMARK", "object": map[string]any{
							"apiVersion": "scheduling.k8s.io/" + tc.version, "kind": "PodGroup",
							"metadata": map[string]any{"resourceVersion": "12", "annotations": map[string]any{"k8s.io/initial-events-end": "true"}},
						}})
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"apiVersion": "scheduling.k8s.io/" + tc.version, "kind": "PodGroupList",
						"metadata": map[string]any{"resourceVersion": "12"}, "items": []any{group},
					})
				default:
					status, reason := http.StatusNotFound, metav1.StatusReasonNotFound
					if tc.status == http.StatusForbidden {
						status, reason = tc.status, metav1.StatusReasonForbidden
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(metav1.Status{
						TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
						Status:   metav1.StatusFailure, Reason: reason, Message: string(reason),
					})
				}
			}))
			defer server.Close()
			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			if err := os.WriteFile(kubeconfig, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
current-context: test
`, server.URL)), 0o600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(t.TempDir(), "scheduler.yaml")
			if err := os.WriteFile(config, []byte(fmt.Sprintf(`apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
clientConnection:
  kubeconfig: %s
leaderElection:
  leaderElect: false
profiles:
- schedulerName: test-scheduler
  plugins:
    preEnqueue:
      enabled:
      - name: ClientProbe
`, kubeconfig)), 0o600); err != nil {
				t.Fatal(err)
			}
			opts := options.NewOptions()
			opts.ConfigFile = config
			opts.SecureServing.BindPort = 0
			informerName, err := cache.NewInformerName(t.Name())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(informerName.Release)
			opts.InformerName = informerName
			var handle fwk.Handle
			cc, sched, err := setup(t.Context(), opts, app.WithPlugin("ClientProbe", func(_ context.Context, _ runtime.Object, h fwk.Handle) (fwk.Plugin, error) {
				handle = h
				return &clientProbe{}, nil
			}))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
					t.Fatalf("setup error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cc.EventBroadcaster.Shutdown)
			t.Cleanup(func() { _ = sched.Profiles.Close() })
			if handle == nil || handle.ClientSet() != cc.Client || handle.SharedInformerFactory() != cc.InformerFactory {
				t.Fatal("framework consumers do not share the configured client and informer factory")
			}
			// Exercise the active Pod store, so a discarded informer retaining the
			// metrics reservation cannot make this check pass.
			if err := cc.InformerFactory.Core().V1().Pods().Informer().GetStore().Replace(nil, "42"); err != nil {
				t.Fatal(err)
			}
			assertStoreResourceVersionMetric(t, informerName.Name(), "pods", 42)
			if !tc.enabled {
				if requests.Load() != 0 {
					t.Fatal("disabled native scheduling must not discover PodGroups")
				}
				return
			}
			pg, err := handle.ClientSet().SchedulingV1beta1().PodGroups("test").Get(t.Context(), "gang", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if pg.Name != "gang" || pg.Spec.SchedulingPolicy.Gang == nil || pg.Spec.SchedulingPolicy.Gang.MinCount != 2 {
				t.Fatalf("framework received an incorrect PodGroup: %#v", pg)
			}
			groups := cc.InformerFactory.Scheduling().V1beta1().PodGroups()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			done := make(chan struct{})
			go func() {
				defer close(done)
				groups.Informer().RunWithContext(ctx)
			}()
			defer func() { cancel(); <-done }()
			if !cache.WaitForCacheSync(ctx.Done(), groups.Informer().HasSynced) {
				t.Fatal("adapted PodGroup informer failed to sync")
			}
			cached, err := groups.Lister().PodGroups("test").Get("gang")
			if err != nil || cached.Spec.SchedulingPolicy.Gang == nil || cached.Spec.SchedulingPolicy.Gang.MinCount != 2 {
				t.Fatalf("informer received an incorrect PodGroup: %#v, %v", cached, err)
			}
			assertStoreResourceVersionMetric(t, informerName.Name(), "podgroups", 12)
		})
	}
}

func assertStoreResourceVersionMetric(t *testing.T, name, resource string, want float64) {
	t.Helper()
	families, err := legacyregistry.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "informer_store_resource_version" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string)
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["name"] == name && labels["resource"] == resource {
				if got := metric.GetGauge().GetValue(); got != want {
					t.Errorf("%s informer resource version metric = %v, want %v", resource, got, want)
				}
				return
			}
		}
	}
	t.Fatalf("missing %s informer resource version metric for %s", resource, name)
}
