// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type lifecyclePlugin struct {
	clientProbe
	closed atomic.Int32
}

func (p *lifecyclePlugin) Close() error {
	p.closed.Add(1)
	return nil
}

func TestRunnableManagerLifecycle(t *testing.T) {
	// This server exercises leadership and core informers; native API adaptation
	// is covered separately by TestSetupPodGroupClient.
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.GenericWorkload, false)
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	for _, tc := range []struct {
		name                                                        string
		delayCache, blockCache, stayStandby, failPlugin, noElection bool
	}{
		{name: "cold standby takeover", delayCache: true},
		{name: "warm standby takeover"},
		{name: "standby shutdown", delayCache: true, stayStandby: true},
		{name: "plugin failure", delayCache: true, failPlugin: true},
		{name: "cache synchronization interrupted", blockCache: true},
		{name: "election disabled", delayCache: true, noElection: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, leaseRequests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if req.URL.Path == "/apis/events.k8s.io/v1" {
					_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"events.k8s.io/v1","resources":[]}`))
					return
				}
				requests.Add(1)
				if strings.Contains(req.URL.Path, "/leases") {
					leaseRequests.Add(1)
				}
				if tc.blockCache || req.URL.Query().Get("watch") == "true" {
					<-req.Context().Done()
					return
				}
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"List","metadata":{"resourceVersion":"1"},"items":[]}`))
			}))
			defer api.Close()
			initializing, initialize := make(chan struct{}), make(chan struct{})
			plugin := &lifecyclePlugin{}
			config := schedulerConfigFile(t, fmt.Sprintf(`delayCacheUntilActive: %t
leaderElection:
  leaderElect: true
  resourceName: ignored
  resourceNamespace: ignored
profiles:
- schedulerName: test-scheduler
  plugins:
    preEnqueue:
      enabled:
      - name: ClientProbe
`, tc.delayCache))
			runnable, err := NewRunnable(config, "", app.WithPlugin("ClientProbe", func(ctx context.Context, _ runtime.Object, _ fwk.Handle) (fwk.Plugin, error) {
				close(initializing)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-initialize:
				}
				if tc.failPlugin {
					return nil, fmt.Errorf("plugin initialization failed")
				}
				return plugin, nil
			}))
			require.NoError(t, err)
			runnable.opts.Master = api.URL
			lease := &coordinationv1.Lease{
				ObjectMeta: metav1.ObjectMeta{Name: "scheduler", Namespace: "default"},
				Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr.To("existing-leader"), LeaseDurationSeconds: ptr.To(int32(600))},
			}
			leases := fake.NewClientset(lease).CoordinationV1()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			addr := listener.Addr().String()
			require.NoError(t, listener.Close())
			mgr, err := ctrl.NewManager(&rest.Config{Host: api.URL}, ctrl.Options{
				LeaderElection: !tc.noElection,
				LeaderElectionResourceLockInterface: &resourcelock.LeaseLock{
					LeaseMeta: lease.ObjectMeta, Client: leases, LockConfig: resourcelock.ResourceLockConfig{Identity: "candidate"},
				},
				RetryPeriod:            ptr.To(100 * time.Millisecond),
				HealthProbeBindAddress: addr, Metrics: metricsserver.Options{BindAddress: "0"},
			})
			require.NoError(t, err)
			require.NoError(t, runnable.SetupWithManager(mgr))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- mgr.Start(ctx) }()
			select {
			case <-initializing:
			case <-time.After(10 * time.Second):
				t.Fatal("scheduler did not initialize before election")
			}
			assertReady := func(want int) {
				t.Helper()
				require.Eventually(t, func() bool {
					response, err := http.Get("http://" + addr + "/readyz")
					if err != nil {
						return false
					}
					defer response.Body.Close()
					return response.StatusCode == want
				}, 10*time.Second, 10*time.Millisecond)
			}
			assertReady(http.StatusInternalServerError)
			close(initialize)
			if !tc.failPlugin {
				readyStatus := http.StatusOK
				if tc.blockCache {
					readyStatus = http.StatusInternalServerError
					require.Eventually(t, func() bool { return requests.Load() > 0 }, 10*time.Second, 10*time.Millisecond)
				}
				assertReady(readyStatus)
				if !tc.noElection {
					select {
					case <-mgr.Elected():
						t.Fatal("standby unexpectedly acquired leadership")
					default:
					}
					if tc.delayCache {
						require.Zero(t, requests.Load(), "cold standby must not start informers")
					}
					if !tc.stayStandby {
						require.NoError(t, leases.Leases("default").Delete(ctx, lease.Name, metav1.DeleteOptions{}))
						select {
						case <-mgr.Elected():
						case <-time.After(10 * time.Second):
							t.Fatal("scheduler did not take over leadership")
						}
						require.Eventually(t, func() bool { return requests.Load() > 0 }, 10*time.Second, 10*time.Millisecond)
						assertReady(readyStatus)
					}
				}
				cancel()
			}
			select {
			case err := <-done:
				if tc.failPlugin {
					require.ErrorContains(t, err, "plugin initialization failed")
				} else {
					require.NoError(t, err)
					require.EqualValues(t, 1, plugin.closed.Load())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("scheduler did not stop")
			}
			require.Error(t, runnable.Check(nil), "stopped scheduler must not report ready")
			require.Zero(t, leaseRequests.Load(), "scheduler must not acquire an independent lease")
		})
	}
}
