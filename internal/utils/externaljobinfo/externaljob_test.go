// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package externaljobinfo

import (
	"encoding/json"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestExternalJobInfo_Equal(t *testing.T) {
	type fields struct {
		Pods []string
	}
	type args struct {
		cmp ExternalJobInfo
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   bool
	}{
		{
			name: "Empty",
			fields: fields{
				Pods: []string{},
			},
			args: args{
				cmp: ExternalJobInfo{
					Pods: []string{},
				},
			},
			want: true,
		},
		{
			name: "Equal",
			fields: fields{
				Pods: []string{"bar/foo"},
			},
			args: args{
				cmp: ExternalJobInfo{
					Pods: []string{"bar/foo"},
				},
			},
			want: true,
		},
		{
			name: "Not equal",
			fields: fields{
				Pods: []string{"bar/foo"},
			},
			args: args{
				cmp: ExternalJobInfo{
					Pods: []string{"buz/biz"},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extInfo := &ExternalJobInfo{
				Pods: tt.fields.Pods,
			}
			if got := extInfo.Equal(tt.args.cmp); got != tt.want {
				t.Errorf("ExternalJobInfo.Equal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExternalJobInfo_ToString(t *testing.T) {
	type fields struct {
		Pods []string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "Empty",
			fields: fields{
				Pods: []string{},
			},
			want: `{"pods":""}`,
		},
		{
			name: "With a pod",
			fields: fields{
				Pods: []string{"bar/foo"},
			},
			want: `{"pods":"bar/foo"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podInfo := &ExternalJobInfo{
				Pods: tt.fields.Pods,
			}
			if got := podInfo.ToString(); got != tt.want {
				t.Errorf("ExternalJobInfo.ToString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodIdentityEncoding(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "slurm-bridge", Name: "job-sleep-single-8wtc2", UID: "4035b798-e371-4b45-8d84-c9026568c7ec",
	}}
	info := ExternalJobInfo{Pods: []string{PodKey(pod), "default/member/member-uid"}}
	wire := info.ToString()
	want := `{"pods":"slurm-bridge/job-sleep-single-8wtc2/4035b798-e371-4b45-8d84-c9026568c7ec,default/member/member-uid"}`
	if wire != want {
		t.Fatalf("encoded comment = %s, want %s", wire, want)
	}
	var decoded ExternalJobInfo
	if err := ParseIntoExternalJobInfo(&wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(decoded.Pods, info.Pods) {
		t.Fatalf("decoded identities = %v, want %v", decoded.Pods, info.Pods)
	}
	ref := ParsePodKey(decoded.Pods[0])
	if ref.Namespace != pod.Namespace || ref.Name != pod.Name || ref.UID != pod.UID {
		t.Errorf("decoded reference = %+v, want pod %s/%s with UID %s", ref, pod.Namespace, pod.Name, pod.UID)
	}
	var legacy struct {
		Pods []string `json:"pods"`
	}
	if err := json.Unmarshal([]byte(wire), &legacy); err == nil {
		t.Fatal("old readers must reject compact identities instead of looking up incorrect pod names")
	}
}

func TestParseIntoExternalJobInfo(t *testing.T) {
	type args struct {
		str *string
		out *ExternalJobInfo
	}
	tests := []struct {
		name    string
		args    args
		want    *ExternalJobInfo
		wantErr bool
	}{
		{
			name: "Empty string",
			args: args{
				str: ptr.To(""),
				out: &ExternalJobInfo{},
			},
			want:    &ExternalJobInfo{},
			wantErr: true,
		},
		{
			name: "Empty values",
			args: args{
				str: ptr.To(`{"pods":[]}`),
				out: &ExternalJobInfo{},
			},
			want:    &ExternalJobInfo{Pods: []string{}},
			wantErr: false,
		},
		{
			name: "Single pod",
			args: args{
				str: ptr.To(`{"pods":["bar/foo"]}`),
				out: &ExternalJobInfo{},
			},
			want:    &ExternalJobInfo{Pods: []string{"bar/foo"}},
			wantErr: false,
		},
		{
			name: "Compact pod identities",
			args: args{
				str: ptr.To(`{"pods":"bar/foo/foo-uid,bar/member/member-uid"}`),
				out: &ExternalJobInfo{},
			},
			want: &ExternalJobInfo{Pods: []string{"bar/foo/foo-uid", "bar/member/member-uid"}},
		},
		{
			name: "Earlier UID map",
			args: args{
				str: ptr.To(`{"pods":["bar/foo","bar/member"],"podUIDs":{"bar/foo":"foo-uid"}}`),
				out: &ExternalJobInfo{},
			},
			want: &ExternalJobInfo{Pods: []string{"bar/foo/foo-uid", "bar/member"}},
		},
		{
			name: "Empty compact list",
			args: args{
				str: ptr.To(`{"pods":""}`),
				out: &ExternalJobInfo{},
			},
			want: &ExternalJobInfo{Pods: []string{}},
		},
		{
			name: "Invalid pods field",
			args: args{
				str: ptr.To(`{"pods":17}`),
				out: &ExternalJobInfo{},
			},
			want: &ExternalJobInfo{}, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ParseIntoExternalJobInfo(tt.args.str, tt.args.out); (err != nil) != tt.wantErr {
				t.Errorf("ParseIntoExternalJobInfo() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got := tt.args.out; !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("ParseIntoExternalJobInfo() = %v, want %v", got, tt.want)
			}
		})
	}
}
