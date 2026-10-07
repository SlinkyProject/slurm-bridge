// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"
)

func TestTerminateHetJobComponent(t *testing.T) {
	tests := []struct {
		name        string
		offset      int32
		status      int
		body        string
		contentType string
		wantError   string
	}{
		{name: "leader component", status: 200, body: `{}`, offset: 0},
		{name: "other component", status: 200, body: `{"errors":[]}`, offset: 1},
		{name: "already absent", status: 404, body: `{"errors":[{"error":"missing"}]}`, offset: 1},
		{name: "HTTP failure", status: 503, body: `{}`, offset: 1, wantError: "HTTP 503"},
		{name: "Slurm failure despite HTTP success", status: 200, body: `{"errors":[{"error":"permission denied","error_number":2000,"description":"wrong user","source":"slurmctld"},{"error":"second error"}]}`, offset: 1, wantError: "second error"},
		{name: "Slurm failure on HTTP error", status: 500, body: `{"errors":[{"description":"controller failed","error_number":123}]}`, offset: 1, wantError: "controller failed"},
		{name: "empty Slurm error", status: 200, body: `{"errors":[{}]}`, offset: 1, wantError: "slurm error"},
		{name: "missing JSON", status: 200, body: ``, contentType: "text/plain", offset: 1, wantError: "missing JSON"},
		{name: "malformed JSON", status: 200, body: `{`, offset: 1, wantError: "terminate heterogeneous job component"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var uri, method, auth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uri, method, auth = r.RequestURI, r.Method, r.Header.Get("X-SLURM-USER-TOKEN")
				contentType := tt.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			cl, err := client.NewClient(&client.Config{Server: server.URL, TokenProvider: token.StaticProvider("test-token"), HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			err = NewControl(cl).TerminateHetJobComponent(context.Background(), 93, tt.offset)
			if (err != nil) != (tt.wantError != "") || (err != nil && !strings.Contains(err.Error(), tt.wantError)) {
				t.Errorf("TerminateHetJobComponent(93, %d) error = %v, want %q", tt.offset, err, tt.wantError)
			}
			wantURI := "/slurm/v0.0.44/job/93%2B1"
			if tt.offset == 0 {
				wantURI = "/slurm/v0.0.44/job/93%2B0"
			}
			if uri != wantURI || method != http.MethodDelete || auth != "test-token" {
				t.Errorf("request = %s %s, token %q; want DELETE %s with configured token", method, uri, auth, wantURI)
			}
			if tt.name == "Slurm failure despite HTTP success" && err != nil && !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("error = %v, want both Slurm errors", err)
			}
		})
	}
}

func TestTerminateHetJobComponentUnavailableAndInvalid(t *testing.T) {
	r := NewControl(fake.NewFakeClient())
	for _, ids := range [][2]int32{{0, 0}, {-1, 0}, {93, -1}, {93, 1}} {
		if err := r.TerminateHetJobComponent(context.Background(), ids[0], ids[1]); err == nil {
			t.Errorf("TerminateHetJobComponent(%d, %d) error = nil, want validation or unavailable-client error", ids[0], ids[1])
		}
	}
}

func TestTerminateHetJobComponentTransportError(t *testing.T) {
	cl, err := client.NewClient(&client.Config{Server: "http://localhost", TokenProvider: token.StaticProvider("test-token")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewControl(cl).TerminateHetJobComponent(ctx, 93, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("TerminateHetJobComponent() error = %v, want context.Canceled", err)
	}
}
