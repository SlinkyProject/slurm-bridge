// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"

	registrationcontrol "github.com/SlinkyProject/slurm-bridge/internal/controller/registration/slurmcontrol"
	schedulercontrol "github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestJobCancellationDoesNotDependOnReadback(t *testing.T) {
	for _, caller := range []struct {
		name   string
		cancel func(context.Context, client.Client) error
	}{
		{name: "allocation cleanup", cancel: func(ctx context.Context, c client.Client) error {
			return registrationcontrol.NewControl(c).TerminateJob(ctx, 42)
		}},
		{name: "scheduler cancellation", cancel: func(ctx context.Context, c client.Client) error {
			control, err := schedulercontrol.NewControl(c, c.GetInformer(api.ObjectTypeV0044JobInfo), "", "")
			if err != nil {
				return err
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{wellknown.LabelExternalJobId: "42"}}}
			return control.DeleteJob(ctx, pod)
		}},
	} {
		for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			t.Run(caller.name+"/"+http.StatusText(status), func(t *testing.T) {
				var mu sync.Mutex
				var requests []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					requests = append(requests, r.Method+" "+r.URL.Path)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodDelete {
						w.WriteHeader(status)
					} else {
						// An acknowledged cancellation must succeed even when reads fail.
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					_, _ = w.Write([]byte(`{}`))
				}))
				t.Cleanup(server.Close)
				c, err := client.NewClient(&client.Config{Server: server.URL, TokenProvider: token.StaticProvider("test-token"), HTTPClient: server.Client()})
				if err != nil {
					t.Fatal(err)
				}
				err = caller.cancel(t.Context(), c)
				if status == http.StatusOK && err != nil {
					t.Fatalf("acknowledged cancellation failed: %v", err)
				}
				if status != http.StatusOK && (err == nil || !strings.Contains(err.Error(), http.StatusText(status))) {
					t.Fatalf("cancellation error = %v; want %s", err, http.StatusText(status))
				}
				mu.Lock()
				defer mu.Unlock()
				want := []string{"DELETE /slurm/v0.0.44/job/42"}
				if caller.name == "scheduler cancellation" && status != http.StatusOK {
					// Failed cancellation needs a live check to recover an allocation
					// that was already released on an earlier scheduling attempt.
					want = append(want, "GET /slurm/v0.0.44/job/42")
				}
				if !reflect.DeepEqual(requests, want) {
					t.Errorf("requests = %v, want %v", requests, want)
				}
			})
		}
	}
}
