// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"
)

func TestTerminateHetJobComponent(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "success", body: `{}`},
		{name: "Slurm error", body: `{"errors":[{"error":"permission denied"}]}`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var uri string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uri = r.RequestURI
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			cl, err := client.NewClient(&client.Config{Server: server.URL, TokenProvider: token.StaticProvider("test-token"), HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			err = NewControl(cl).TerminateHetJobComponent(context.Background(), 93, 0)
			if (err != nil) != tt.wantErr {
				t.Errorf("TerminateHetJobComponent(93, 0) error = %v, wantErr %t", err, tt.wantErr)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("TerminateHetJobComponent(93, 0) error = %v, want propagated Slurm error", err)
			}
			if uri != "/slurm/v0.0.44/job/93%2B0" {
				t.Errorf("request URI = %q, want encoded leader component", uri)
			}
		})
	}
}
