// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"
)

func TestNodeIndexAvoidsSchedulingLists(t *testing.T) {
	var response atomic.Value
	response.Store(`{"nodes":[{"name":"node1","partitions":["batch","gpu"]},{"name":"node2","partitions":["gpu"]},{"name":"node3","partitions":["other"]}]}`)
	var requests atomic.Int32
	var unavailable atomic.Bool
	c, err := client.NewClient(&client.Config{Server: "http://slurm.test", TokenProvider: token.StaticProvider("test"),
		HTTPClient: &http.Client{Transport: indexTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Path != "/slurm/v0.0.44/nodes/" {
				return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			requests.Add(1)
			if unavailable.Load() {
				return nil, fmt.Errorf("Slurm unavailable")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(response.Load().(string))), Request: r}, nil
		})}}, &client.ClientOptions{CacheSyncPeriod: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControl(c, c.GetInformer(api.ObjectTypeV0044JobInfo), "", "batch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.GetNodeNames(t.Context(), nil); err == nil {
		t.Fatal("unsynchronized inventory must not appear empty")
	}
	if requests.Load() != 0 {
		t.Fatal("lookup before startup issued a live request")
	}
	informer := c.GetInformer(api.ObjectTypeV0044Node)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); informer.Run(stop) }()
	t.Cleanup(func() { close(stop); <-done })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := wait.PollUntilContextCancel(ctx, time.Millisecond, true, func(context.Context) (bool, error) {
		return informer.HasStarted(), nil
	}); err != nil {
		t.Fatal(err)
	}
	refresh := func() error {
		return informer.List(ctx, &api.V0044NodeObjectList{}, &client.ListOptions{RefreshCache: true})
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	check := func(partition *string, want []string) {
		t.Helper()
		before := requests.Load()
		for range 3 {
			got, err := control.GetNodeNames(ctx, partition)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("partition %v: nodes=%v, error=%v; want %v", partition, got, err, want)
			}
		}
		if requests.Load() != before {
			t.Fatal("scheduling lookups issued live requests")
		}
	}
	check(nil, []string{"node1"})
	check(ptr.To("gpu, batch, gpu"), []string{"node1", "node2"})
	check(ptr.To("missing"), []string{})
	// Changes remain invisible until refresh; moves and deletions remove old keys.
	response.Store(`{"nodes":[{"name":"node1","partitions":["other"]},{"name":"node4","partitions":["gpu"]}]}`)
	check(ptr.To("gpu"), []string{"node1", "node2"})
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	check(nil, []string{})
	check(ptr.To("gpu"), []string{"node4"})
	check(ptr.To("other"), []string{"node1"})
	unavailable.Store(true)
	if err := refresh(); err == nil {
		t.Fatal("expected refresh error")
	}
	before := requests.Load()
	if _, err := control.GetNodeNames(ctx, nil); err == nil {
		t.Fatal("failed refresh must propagate an error")
	}
	if requests.Load() != before {
		t.Fatal("failed cache lookup fell back to a live request")
	}
}
