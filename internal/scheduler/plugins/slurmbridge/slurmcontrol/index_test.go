// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/client/token"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func indexedJob(id int32, pods string) *api.V0044JobInfo {
	return &api.V0044JobInfo{
		JobId: ptr.To(id), AdminComment: ptr.To(`{"pods":[` + pods + `]}`),
		JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
		Nodes:    ptr.To("node1"),
	}
}

func TestIndexedJobsConfirmLiveIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		label  string
		uid    types.UID
		cached []*api.V0044JobInfo
		live   []*api.V0044JobInfo
		wantID int32
	}{
		{name: "missing index entry", live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}},
		{name: "deleted job", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}},
		{name: "ownership changed", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/other"`)}},
		{name: "invalid comment", cached: []*api.V0044JobInfo{{JobId: ptr.To(int32(1)), AdminComment: ptr.To("not json")}}},
		{name: "other namespace", cached: []*api.V0044JobInfo{indexedJob(1, `"other/pod"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"other/pod"`)}},
		{name: "matching UID", uid: "current", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod/current"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod/current"`)}, wantID: 1},
		{name: "replaced Pod", uid: "current", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod/old"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod/old"`)}},
		{name: "live UID changed", uid: "current", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod/current"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod/old"`)}},
		{name: "legacy claim for Pod with UID", uid: "current", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}, wantID: 1},
		{name: "unique claim repairs stale label", label: "2", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}, wantID: 1},
		{name: "live labeled allocation is not indexed yet", label: "2", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`), indexedJob(2, `"ns/pod"`)}},
		{name: "ambiguous claims", cached: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`), indexedJob(2, `"ns/pod"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`), indexedJob(2, `"ns/pod"`)}},
		{name: "label cannot select between active claims", label: "1", cached: []*api.V0044JobInfo{indexedJob(2, `"ns/pod"`), indexedJob(1, `"ns/pod"`)}, live: []*api.V0044JobInfo{indexedJob(1, `"ns/pod"`), indexedJob(2, `"ns/pod"`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cached, live := fake.NewClientBuilder(), fake.NewClientBuilder()
			for _, j := range tc.cached {
				cached.WithObjects(j)
			}
			for _, j := range tc.live {
				live.WithObjects(j)
			}
			control, err := NewControl(live.Build(), cached.Build().GetInformer(api.ObjectTypeV0044JobInfo), "", "")
			if err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", UID: tc.uid, Labels: map[string]string{wellknown.LabelExternalJobId: tc.label}}}
			candidates, err := control.GetJobsForPods(t.Context(), pod)
			if err != nil {
				t.Fatal(err)
			}
			got, err := control.VerifyJobsForPods(t.Context(), *candidates, pod)
			if err != nil {
				t.Fatal(err)
			}
			if job, _ := JobForPod(*got, pod); job.JobId != tc.wantID {
				t.Fatalf("jobs = %v, want ID %d", got, tc.wantID)
			}
		})
	}
}

type orderedJobIndex struct {
	client.InformerCache
	jobs []api.V0044JobInfo
}

func (i orderedJobIndex) AddIndex(string, client.IndexFunc) error { return nil }

func (i orderedJobIndex) ByIndex(_ context.Context, _, _ string, list object.ObjectList) error {
	list.(*api.V0044JobInfoObjectList).Items = append(list.(*api.V0044JobInfoObjectList).Items, i.jobs...)
	return nil
}

func TestIndexedJobsIgnoreCandidateOrder(t *testing.T) {
	for _, ids := range [][2]int32{{1, 2}, {2, 1}} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			control, err := NewControl(nil, orderedJobIndex{jobs: []api.V0044JobInfo{
				*indexedJob(ids[0], `"ns/pod"`), *indexedJob(ids[1], `"ns/pod"`),
			}}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []int32{0, 1, 2} {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod", Labels: map[string]string{}}}
				if id != 0 {
					pod.Labels[wellknown.LabelExternalJobId] = fmt.Sprint(id)
				}
				jobs, err := control.GetJobsForPods(t.Context(), pod)
				if err != nil {
					t.Fatal(err)
				}
				job, found := JobForPod(*jobs, pod)
				if found || job.JobId != 0 {
					t.Fatalf("label %q selected an ambiguous allocation: %v", pod.Labels[wellknown.LabelExternalJobId], jobs)
				}
			}
		})
	}
}

type failingJobIndex struct{ client.InformerCache }

var errIndexUnavailable = errors.New("job index unavailable")

func (f failingJobIndex) ByIndex(context.Context, string, string, object.ObjectList) error {
	return errIndexUnavailable
}

func TestIndexedJobsErrorsAndSharedReads(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod"}}
	member := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "member"}}
	for _, mode := range []string{"index error", "live error", "wrong component", "stale terminal state"} {
		t.Run(mode, func(t *testing.T) {
			job := indexedJob(1, `"ns/pod","ns/member"`)
			job.JobState = &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCOMPLETED}
			informer := fake.NewClientBuilder().WithObjects(job).Build().GetInformer(api.ObjectTypeV0044JobInfo)
			if mode == "index error" {
				informer = failingJobIndex{informer}
			}
			reads := 0
			live := interceptor.NewClient(fake.NewFakeClient(), interceptor.Funcs{
				Get: func(_ context.Context, _ object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
					reads++
					if !(new(client.GetOptions).ApplyOptions(opts)).SkipCache {
						t.Error("verification did not bypass cache")
					}
					if mode == "live error" {
						return errIndexUnavailable
					}
					fresh := indexedJob(1, `"ns/pod","ns/member"`)
					if mode == "wrong component" {
						fresh.JobId = ptr.To(int32(2))
					}
					*obj.(*api.V0044JobInfo) = *fresh
					return nil
				},
			})
			control, err := NewControl(live, informer, "", "")
			if err != nil {
				t.Fatal(err)
			}
			got, err := control.GetJobsForPods(t.Context(), pod, member)
			if mode == "index error" {
				if !errors.Is(err, errIndexUnavailable) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reads != 0 {
				t.Fatalf("cached lookup made %d live reads", reads)
			}
			got, err = control.VerifyJobsForPods(t.Context(), *got, pod, member)
			if mode == "live error" {
				if !errors.Is(err, errIndexUnavailable) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reads != 1 {
				t.Fatalf("reads = %d, want one per shared job", reads)
			}
			if mode == "wrong component" {
				if len(*got) != 0 {
					t.Fatalf("accepted wrong component: %v", got)
				}
				return
			}
			if len(*got) != 2 || (*got)["ns/pod"].Finished || (*got)["ns/member"].Finished {
				t.Fatalf("expected both Pods to have the live running job, got %v", got)
			}
		})
	}
}

type indexTransport func(*http.Request) (*http.Response, error)

func (f indexTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Use the production informer to prove repeated scheduling lookups do not list
// unrelated jobs, and that refreshing it updates and removes ownership keys.
func TestJobIndexAvoidsSchedulingLists(t *testing.T) {
	jobs := []api.V0044JobInfo{*indexedJob(7, `"ns/pod","ns/member","ns/pod"`)}
	for n := range 3 {
		jobs = append(jobs, *indexedJob(int32(n+100), `"ns/unrelated"`))
	}
	body, err := json.Marshal(map[string]any{"jobs": jobs})
	if err != nil {
		t.Fatal(err)
	}
	var response atomic.Value
	response.Store(string(body))
	var lists, gets atomic.Int32
	config := &client.Config{
		Server: "http://slurm.example", TokenProvider: token.StaticProvider("test"),
		HTTPClient: &http.Client{Transport: indexTransport(func(r *http.Request) (*http.Response, error) {
			var body string
			switch r.URL.Path {
			case "/slurm/v0.0.44/jobs/":
				lists.Add(1)
				body = response.Load().(string)
			case "/slurm/v0.0.44/job/7", "/slurm/v0.0.44/job/9":
				gets.Add(1)
				body = `{"jobs":[{"job_id":7,"admin_comment":"{\"pods\":[\"ns/pod\",\"ns/member\"]}","nodes":"node2","job_state":["RUNNING"]}]}`
			case "/slurm/v0.0.44/job/8":
				gets.Add(1)
				body = `{"jobs":[{"job_id":8,"het_job_id":{"set":true,"number":7},"het_job_offset":{"set":true,"number":1},"nodes":"node2","job_state":["RUNNING"]}]}`
			default:
				return nil, fmt.Errorf("unexpected request %s", r.URL.Path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
	}
	c, err := client.NewClient(config, &client.ClientOptions{CacheSyncPeriod: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	informer := c.GetInformer(api.ObjectTypeV0044JobInfo)
	control, err := NewControl(c, informer, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewControl(c, informer, "", ""); err == nil {
		t.Fatal("duplicate index registration succeeded")
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "pod"}}
	if _, err := control.GetJobsForPods(t.Context(), pod); err == nil {
		t.Fatal("lookup before informer startup succeeded")
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); informer.Run(stop) }()
	t.Cleanup(func() { close(stop); <-done })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for !informer.HasStarted() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	refresh := func() {
		t.Helper()
		if err := informer.List(ctx, &api.V0044JobInfoObjectList{}, &client.ListOptions{RefreshCache: true}); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	member := pod.DeepCopy()
	member.Name = "member"
	for range 20 {
		got, err := control.GetJobsForPods(ctx, pod, member)
		if err != nil {
			t.Fatal(err)
		}
		if len(*got) != 2 || (*got)["ns/pod"].JobId != 7 || (*got)["ns/pod"].Nodes != "node1" {
			t.Fatalf("unexpected jobs: %v", got)
		}
	}
	if lists.Load() != 1 || gets.Load() != 0 {
		t.Fatalf("requests: lists=%d gets=%d, want 1/0", lists.Load(), gets.Load())
	}
	candidates, err := control.GetJobsForPods(ctx, pod, member)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.VerifyJobsForPods(ctx, *candidates, pod, member)
	if err != nil || len(*verified) != 2 || (*verified)["ns/pod"].Nodes != "node2" || (*verified)["ns/member"].Nodes != "node2" {
		t.Fatalf("explicit verification = %v, %v; want node2 for both Pods", verified, err)
	}
	if lists.Load() != 1 || gets.Load() != 1 {
		t.Fatalf("verification requests: lists=%d gets=%d, want 1/1", lists.Load(), gets.Load())
	}
	// Ordinary scheduling reads must also bypass the started informer, whose
	// copy of job 7 still reports node1.
	pod.Labels = map[string]string{wellknown.LabelExternalJobId: "7"}
	job, err := control.GetJob(ctx, pod)
	if err != nil || job.Nodes != "node2" {
		t.Fatalf("live job lookup = %v, %v; want node2", job, err)
	}
	// A component absent from the cache is still fetched directly by its own ID.
	pod.Labels[wellknown.LabelExternalJobId] = "8"
	job, err = control.GetJob(ctx, pod)
	if err != nil || job.JobId != 8 || job.HetJobId != 7 || job.HetJobOffset != 1 || job.Nodes != "node2" {
		t.Fatalf("live component lookup = %v, %v; want job 8 on node2", job, err)
	}
	// An unexpected identity is an error, not a reason to scan every Slurm job.
	pod.Labels[wellknown.LabelExternalJobId] = "9"
	job, err = control.GetJob(ctx, pod)
	if err == nil || !strings.Contains(err.Error(), "slurm returned job 7 when requesting job 9") || job != nil {
		t.Fatalf("mismatched job lookup = %v, %v; want an identity error", job, err)
	}
	if lists.Load() != 1 || gets.Load() != 4 {
		t.Fatalf("requests after scheduling reads: lists=%d gets=%d, want 1/4", lists.Load(), gets.Load())
	}
	for _, body := range []string{
		`{"jobs":[{"job_id":7,"admin_comment":"{\"pods\":[\"ns/renamed\"]}"}]}`,
		`{"jobs":[]}`,
	} {
		response.Store(body)
		refresh()
		old := &api.V0044JobInfoObjectList{}
		if err := informer.ByIndex(ctx, slurmjob.PodJobIndex, "ns/pod", old); err != nil {
			t.Fatal(err)
		}
		if len(old.Items) != 0 {
			t.Fatal("old ownership survived refresh")
		}
		renamed := &api.V0044JobInfoObjectList{}
		if err := informer.ByIndex(ctx, slurmjob.PodJobIndex, "ns/renamed", renamed); err != nil {
			t.Fatal(err)
		}
		want := 1
		if body == `{"jobs":[]}` {
			want = 0
		}
		if len(renamed.Items) != want {
			t.Fatalf("renamed entries = %d, want %d", len(renamed.Items), want)
		}
	}
}
