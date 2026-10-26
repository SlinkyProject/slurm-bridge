// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
)

func TestDuplicateCancellationDoesNotDependOnReadback(t *testing.T) {
	info := externaljobinfo.ExternalJobInfo{Pods: []string{"ns/pod/uid"}}
	job := func(id int32) api.V0044JobInfo {
		return api.V0044JobInfo{JobId: ptr.To(id), AdminComment: ptr.To(info.ToString()),
			JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING}}
	}
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var mu sync.Mutex
			var requests []string
			deleted := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodDelete:
					deleted = true
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{}`))
				case r.Method == http.MethodGet && !deleted:
					id, err := strconv.ParseInt(path.Base(r.URL.Path), 10, 32)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []api.V0044JobInfo{job(int32(id))}})
				default:
					// Reads must verify ownership before cancellation, never follow it.
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			t.Cleanup(server.Close)
			c, err := slurmclient.NewClient(&slurmclient.Config{Server: server.URL,
				TokenProvider: token.StaticProvider("test"), HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{slurmClient: c}
			allocations := map[int32][]api.V0044JobInfo{10: {job(10)}, 20: {job(20)}}
			err = r.cleanAllocationConflict(t.Context(), "pod/ns/pod/uid", allocations, make(map[int32]bool))
			if status == http.StatusOK && err != nil {
				t.Fatalf("acknowledged cancellation failed: %v", err)
			}
			if status != http.StatusOK && (err == nil || !strings.Contains(err.Error(), http.StatusText(status))) {
				t.Fatalf("cancellation error = %v; want %s", err, http.StatusText(status))
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 3 {
				t.Fatalf("requests = %v; want two live reads and one cancellation", requests)
			}
			slices.Sort(requests[:2])
			want := []string{"GET /slurm/v0.0.44/job/10", "GET /slurm/v0.0.44/job/20", "DELETE /slurm/v0.0.44/job/20"}
			if !reflect.DeepEqual(requests, want) {
				t.Fatalf("requests = %v, want %v", requests, want)
			}
		})
	}
}
