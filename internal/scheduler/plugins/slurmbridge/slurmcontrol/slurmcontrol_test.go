// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	"k8s.io/utils/lru"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func Test_sharedFromExclusiveAnnotation(t *testing.T) {
	tests := []struct {
		name              string
		slurmJobComponent *slurmjobir.SlurmJobComponent
		wantShared        api.V0044JobDescMsgShared
	}{
		{
			name:              "nil component defaults to exclusive",
			slurmJobComponent: nil,
			wantShared:        api.V0044JobDescMsgSharedNone,
		},
		{
			name:              "component with Exclusive nil defaults to exclusive",
			slurmJobComponent: &slurmjobir.SlurmJobComponent{},
			wantShared:        api.V0044JobDescMsgSharedNone,
		},
		{
			name:              "component Exclusive true",
			slurmJobComponent: &slurmjobir.SlurmJobComponent{JobInfo: slurmjobir.SlurmJobIRJobInfo{Exclusive: ptr.To(true)}},
			wantShared:        api.V0044JobDescMsgSharedNone,
		},
		{
			name:              "component Exclusive false uses MCS sharing",
			slurmJobComponent: &slurmjobir.SlurmJobComponent{JobInfo: slurmjobir.SlurmJobIRJobInfo{Exclusive: ptr.To(false)}},
			wantShared:        api.V0044JobDescMsgSharedMcs,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sharedFromExclusiveAnnotation(tt.slurmJobComponent)
			if got == nil || len(*got) != 1 || (*got)[0] != tt.wantShared {
				t.Errorf("sharedFromExclusiveAnnotation() = %v, want [%v]", got, tt.wantShared)
			}
		})
	}
}

func Test_gresCompatibilityConstraint(t *testing.T) {
	tests := []struct {
		name        string
		constraints *string
		want        string
		wantErr     bool
	}{
		{
			name: "adds required feature without user constraints",
			want: wellknown.SlurmFeatureGRESCompatible,
		},
		{
			name:        "combines required feature with user constraints",
			constraints: ptr.To("gpu&(rack-a|rack-b)"),
			want:        wellknown.SlurmFeatureGRESCompatible + "&gpu&(rack-a|rack-b)",
		},
		{
			name:        "groups a top-level OR",
			constraints: ptr.To("rack-a|rack-b"),
			want:        wellknown.SlurmFeatureGRESCompatible + "&(rack-a|rack-b)",
		},
		{
			name:        "keeps matching-OR brackets",
			constraints: ptr.To("[rack-a|rack-b]"),
			want:        wellknown.SlurmFeatureGRESCompatible + "&[rack-a|rack-b]",
		},
		{
			name:        "rejects an expression that cannot be composed",
			constraints: ptr.To("(a&b)|(c&d)"),
			wantErr:     true,
		},
		{
			name:        "does not duplicate the required feature",
			constraints: ptr.To(wellknown.SlurmFeatureGRESCompatible),
			want:        wellknown.SlurmFeatureGRESCompatible,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gresCompatibilityConstraint(tt.constraints)
			if (err != nil) != tt.wantErr {
				t.Fatalf("gresCompatibilityConstraint() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && ptr.Deref(got, "") != tt.want {
				t.Errorf("gresCompatibilityConstraint() = %q, want %q", ptr.Deref(got, ""), tt.want)
			}
		})
	}
}

func Test_realSlurmControl_DeleteJob(t *testing.T) {
	type fields struct {
		Client    client.Client
		mcsLabel  string
		partition string
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "No jobs to delete",
			fields: fields{
				Client: func() client.Client {
					return fake.NewClientBuilder().
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{},
			},
			wantErr: false,
		},
		{
			name: "Delete job that does not exist",
			fields: fields{
				Client: func() client.Client {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								JobId: ptr.To[int32](2),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Delete job",
			fields: fields{
				Client: func() client.Client {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								JobId: ptr.To[int32](1),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				lastUpdate: lru.New(10),
				Client:     tt.fields.Client,
				mcsLabel:   tt.fields.mcsLabel,
				partition:  tt.fields.partition,
			}
			if err := r.DeleteJob(tt.args.ctx, tt.args.pod); (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.DeleteJob() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeleteJobConfirmsReleaseAfterCancellationError(t *testing.T) {
	deleteErr := errors.New("cancellation failed")
	lookupErr := errors.New("lookup unavailable")
	for _, tt := range []struct {
		name      string
		state     api.V0044JobInfoJobState
		lookupErr error
		wantErr   bool
	}{
		{name: "canceled", state: api.V0044JobInfoJobStateCANCELLED},
		{name: "completed", state: api.V0044JobInfoJobStateCOMPLETED},
		{name: "running", state: api.V0044JobInfoJobStateRUNNING, wantErr: true},
		{name: "pending", state: api.V0044JobInfoJobStatePENDING, wantErr: true},
		{name: "lookup fails", lookupErr: lookupErr, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job := &api.V0044JobInfo{
				JobId: ptr.To(int32(1)), JobState: &[]api.V0044JobInfoJobState{tt.state},
			}
			base := fake.NewClientBuilder().WithObjects(job).Build()
			c := interceptor.NewClient(base, interceptor.Funcs{
				Delete: func(context.Context, object.Object, ...client.DeleteOption) error {
					return deleteErr
				},
				Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
					if tt.lookupErr != nil {
						return tt.lookupErr
					}
					return base.Get(ctx, key, obj, opts...)
				},
			})
			pod := &corev1.Pod{ObjectMeta: v1.ObjectMeta{Labels: map[string]string{wellknown.LabelExternalJobId: "1"}}}
			err := (&realSlurmControl{Client: c, lastUpdate: lru.New(10)}).DeleteJob(t.Context(), pod)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DeleteJob() error = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, deleteErr) {
				t.Fatalf("DeleteJob() error = %v, want cancellation error preserved", err)
			}
			if tt.lookupErr != nil && !errors.Is(err, tt.lookupErr) {
				t.Fatalf("DeleteJob() error = %v, want lookup error preserved", err)
			}
		})
	}
}

func Test_realSlurmControl_GetJobsForPods(t *testing.T) {
	type fields struct {
		Client client.Client
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *map[string]ExternalJob
		wantErr bool
	}{
		{
			name: "No jobs in slurm",
			fields: fields{
				Client: func() client.Client {
					return fake.NewClientBuilder().
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want:    &map[string]ExternalJob{},
			wantErr: false,
		},
		{
			name: "List jobs",
			fields: fields{
				Client: func() client.Client {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1, node2"),
							},
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod2"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](2),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
								Nodes:    ptr.To(""),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want: &map[string]ExternalJob{
				"slurm/pod1": {JobId: 1, Nodes: "node1, node2", Pending: false},
				"slurm/pod2": {JobId: 2, Nodes: "", Pending: true},
			},
			wantErr: false,
		},
		{
			name: "Ambiguous jobs leave metadata untouched",
			fields: fields{
				Client: func() client.Client {
					adminComment := func() *string {
						pi := externaljobinfo.ExternalJobInfo{
							Pods: []string{"slurm/pod1"},
						}
						return ptr.To(pi.ToString())
					}
					items := []api.V0044JobInfo{
						{
							AdminComment: adminComment(),
							JobId:        ptr.To[int32](1),
							JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
							Nodes:        ptr.To("node1"),
						},
						{
							AdminComment: adminComment(),
							JobId:        ptr.To[int32](2),
							JobState:     &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
							Nodes:        ptr.To("node2"),
						},
					}
					return fake.NewClientBuilder().WithLists(&api.V0044JobInfoObjectList{Items: items}).Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want:    &map[string]ExternalJob{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				lastUpdate: lru.New(10),
				Client:     tt.fields.Client,
			}
			control, err := NewControl(r.Client, r.GetInformer(api.ObjectTypeV0044JobInfo), "kubernetes", "slurm-bridge")
			if err != nil {
				t.Fatal(err)
			}
			got, err := control.GetJobsForPods(tt.args.ctx,
				&corev1.Pod{ObjectMeta: v1.ObjectMeta{Namespace: "slurm", Name: "pod1"}},
				&corev1.Pod{ObjectMeta: v1.ObjectMeta{Namespace: "slurm", Name: "pod2"}},
			)
			if (err != nil) != tt.wantErr {
				t.Fatalf("realSlurmControl.GetJobsForPods() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetJobsForPods() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_GetJobsForPodsFinishedJobs(t *testing.T) {
	tests := []struct {
		state    api.V0044JobInfoJobState
		finished bool
	}{
		{api.V0044JobInfoJobStateCOMPLETED, true},
		{api.V0044JobInfoJobStateCANCELLED, true},
		{api.V0044JobInfoJobStateFAILED, true},
		{api.V0044JobInfoJobStateTIMEOUT, true},
		{api.V0044JobInfoJobStateNODEFAIL, true},
		{api.V0044JobInfoJobStatePREEMPTED, true},
		{api.V0044JobInfoJobStateBOOTFAIL, true},
		{api.V0044JobInfoJobStateDEADLINE, true},
		{api.V0044JobInfoJobStateOUTOFMEMORY, true},
		{api.V0044JobInfoJobStateRUNNING, false},
		{api.V0044JobInfoJobStatePENDING, false},
		{api.V0044JobInfoJobStateSUSPENDED, false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			info := externaljobinfo.ExternalJobInfo{Pods: []string{"test/pod"}}
			r := &realSlurmControl{Client: fake.NewClientBuilder().WithObjects(
				&api.V0044JobInfo{
					JobId: ptr.To(int32(2)), JobState: &[]api.V0044JobInfoJobState{tt.state},
					AdminComment: ptr.To(info.ToString()),
					HetJobId:     &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(1))},
					HetJobOffset: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: ptr.To(int32(1))},
				},
			).Build()}
			control, err := NewControl(r.Client, r.GetInformer(api.ObjectTypeV0044JobInfo), "kubernetes", "slurm-bridge")
			if err != nil {
				t.Fatal(err)
			}
			got, err := control.GetJobsForPods(context.Background(), &corev1.Pod{ObjectMeta: v1.ObjectMeta{Namespace: "test", Name: "pod"}})
			if err != nil {
				t.Fatal(err)
			}
			job, ok := (*got)["test/pod"]
			if !ok || job.JobId != 2 || job.HetJobId != 1 || job.Finished != tt.finished {
				t.Errorf("GetJobsForPods() = %v; want component 2 of job 1 with Finished=%t", got, tt.finished)
			}
		})
	}
}

func Test_realSlurmControl_GetJob(t *testing.T) {
	type fields struct {
		Client    client.Client
		partition string
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *ExternalJob
		wantErr bool
	}{
		{
			name: "Failed to get job",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
							return fmt.Errorf("failed to get resource")
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj(),
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Job not found",
			fields: fields{
				Client: func() client.Client {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To(""),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelExternalJobId: "3"}).Obj(),
			},
			want:    &ExternalJob{},
			wantErr: false,
		},
		{
			name: "Zero job ID not found",
			fields: fields{
				Client: fake.NewClientBuilder().
					Build(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelExternalJobId: "0"}).Obj(),
			},
			want:    &ExternalJob{},
			wantErr: false,
		},
		{
			name: "Job not running",
			fields: fields{
				Client: func() client.Client {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/pod1"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateCANCELLED},
								Nodes:    ptr.To(""),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj(),
			},
			want:    &ExternalJob{},
			wantErr: false,
		},
		{
			name: "Job found and running",
			fields: fields{
				Client: func() client.Client {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/foo"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStateRUNNING},
								Nodes:    ptr.To("node1"),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj(),
			},
			want:    &ExternalJob{JobId: 1, Nodes: "node1"},
			wantErr: false,
		},
		{
			name: "Job found and pending",
			fields: fields{
				Client: func() client.Client {
					list := &api.V0044JobInfoObjectList{
						Items: []api.V0044JobInfo{
							{
								AdminComment: func() *string {
									pi := externaljobinfo.ExternalJobInfo{
										Pods: []string{"slurm/foo"},
									}
									return ptr.To(pi.ToString())
								}(),
								JobId:    ptr.To[int32](1),
								JobState: &[]api.V0044JobInfoJobState{api.V0044JobInfoJobStatePENDING},
								Nodes:    ptr.To(""),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(list).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: st.MakePod().Name("foo").Namespace("slurm-bridge").Labels(map[string]string{wellknown.LabelExternalJobId: "1"}).Obj(),
			},
			want:    &ExternalJob{JobId: 1, Nodes: "", Pending: true},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				lastUpdate: lru.New(10),
				Client:     tt.fields.Client,
				partition:  tt.fields.partition,
			}
			got, err := r.GetJob(tt.args.ctx, tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.GetSlurmJob() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetSlurmJob() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_UpdateJobPreservesSharing(t *testing.T) {
	for _, tt := range []struct {
		name      string
		exclusive *bool
	}{
		{name: "default exclusive"},
		{name: "exclusive", exclusive: ptr.To(true)},
		{name: "MCS", exclusive: ptr.To(false)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pod := st.MakePod().Name("pending").Namespace("slurm-bridge").
				Label(wellknown.LabelExternalJobId, "42").Obj()
			ir := &slurmjobir.SlurmJobIR{
				Components: []slurmjobir.SlurmJobComponent{{
					Pods: corev1.PodList{Items: []corev1.Pod{*pod}},
					JobInfo: slurmjobir.SlurmJobIRJobInfo{
						Exclusive:  tt.exclusive,
						Account:    ptr.To("research"),
						CpuPerTask: ptr.To(int32(2)),
						MemPerNode: ptr.To(int64(200)),
						ExcNodes:   []string{"node2"},
					},
				}},
			}
			updates := 0
			r := &realSlurmControl{
				lastUpdate: lru.New(10),
				mcsLabel:   "kubernetes",
				Client: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					Update: func(ctx context.Context, obj object.Object, req any, opts ...client.UpdateOption) error {
						updates++
						if got := ptr.Deref(obj.(*api.V0044JobInfo).JobId, 0); got != 42 {
							t.Errorf("updated job ID = %d, want 42", got)
						}
						desc := req.(api.V0044JobDescMsg)
						payload, err := json.Marshal(desc)
						if err != nil {
							return err
						}
						var fields map[string]json.RawMessage
						if err := json.Unmarshal(payload, &fields); err != nil {
							return err
						}
						// Slurm treats any nonzero shared update as oversubscribe,
						// including MCS. Omit the field to preserve the submitted mode.
						if shared, present := fields["shared"]; present {
							t.Errorf("pending-job update sends shared=%s; want the field omitted to preserve isolation", shared)
						}
						if account, present := fields["account"]; present {
							t.Errorf("pending-job update sends account=%s; want it set only at submission", account)
						}
						if ptr.Deref(desc.McsLabel, "") != "kubernetes" {
							t.Errorf("update lost the MCS label: %v", desc.McsLabel)
						}
						if desc.ExcludedNodes == nil || !slices.Equal(*desc.ExcludedNodes, ir.Components[0].JobInfo.ExcNodes) {
							t.Errorf("excluded nodes were not updated: %v", desc.ExcludedNodes)
						}
						if ptr.Deref(desc.CpusPerTask, 0) != 2 || desc.MemoryPerNode == nil || ptr.Deref(desc.MemoryPerNode.Number, 0) != 200 {
							t.Error("resource requirements were not updated")
						}
						return nil
					},
				}).Build(),
			}
			for _, excluded := range [][]string{{"node2"}, {}} {
				ir.Components[0].JobInfo.ExcNodes = excluded
				if id, err := r.UpdateJob(t.Context(), pod, ir); err != nil || id != 42 {
					t.Fatalf("UpdateJob() = (%d, %v), want (42, nil)", id, err)
				}
			}
			if updates != 2 {
				t.Fatalf("update calls = %d, want 2", updates)
			}
		})
	}
}
func Test_realSlurmControl_GetNodeNames(t *testing.T) {
	type fields struct {
		Client    client.Client
		mcsLabel  string
		partition string
	}
	type args struct {
		ctx       context.Context
		partition *string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "No slurm nodes",
			fields: fields{
				Client: func() client.Client {
					return fake.NewClientBuilder().
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want:    []string{},
			wantErr: false,
		},
		{
			name: "List nodes using configured partitions",
			fields: fields{
				partition: "slurm-bridge, gpu",
				Client: func() client.Client {
					nodes := &api.V0044NodeObjectList{
						Items: []api.V0044Node{
							{
								Name:       ptr.To("node1"),
								Partitions: ptr.To(api.V0044CsvString{"slurm-bridge"}),
							},
							{
								Name:       ptr.To("node2"),
								Partitions: ptr.To(api.V0044CsvString{"other", "gpu"}),
							},
							{
								Name:       ptr.To("node3"),
								Partitions: ptr.To(api.V0044CsvString{"other"}),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(nodes).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
			},
			want:    []string{"node1", "node2"},
			wantErr: false,
		},
		{
			name: "List nodes using job partition overrides",
			fields: fields{
				partition: "slurm-bridge",
				Client: func() client.Client {
					nodes := &api.V0044NodeObjectList{
						Items: []api.V0044Node{
							{
								Name:       ptr.To("node1"),
								Partitions: ptr.To(api.V0044CsvString{"slurm-bridge"}),
							},
							{
								Name:       ptr.To("node2"),
								Partitions: ptr.To(api.V0044CsvString{"other", "gpu"}),
							},
							{
								Name:       ptr.To("node3"),
								Partitions: ptr.To(api.V0044CsvString{"batch"}),
							},
						},
					}
					return fake.NewClientBuilder().
						WithLists(nodes).
						Build()
				}(),
			},
			args: args{
				ctx:       context.Background(),
				partition: ptr.To("gpu,batch"),
			},
			want:    []string{"node2", "node3"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewControl(tt.fields.Client, tt.fields.Client.GetInformer(api.ObjectTypeV0044JobInfo), tt.fields.mcsLabel, tt.fields.partition)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.GetNodeNames(tt.args.ctx, tt.args.partition)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.GetNodeNames() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			slices.Sort(got)
			slices.Sort(tt.want)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetNodeNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_GetResources(t *testing.T) {
	type fields struct {
		Client    client.Client
		mcsLabel  string
		partition string
	}
	type args struct {
		ctx      context.Context
		pod      *corev1.Pod
		nodeName string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *NodeResources
		wantErr bool
	}{
		{
			name: "No JobId",
			fields: fields{
				Client: func() client.Client {
					return fake.NewClientBuilder().
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelExternalJobId: ""},
					},
				},
				nodeName: "",
			},
			want:    &NodeResources{},
			wantErr: false,
		},
		{
			name: "Failed to Get",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
							return fmt.Errorf("failed to get resources")
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
					},
				},
				nodeName: "",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "No data",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
							return nil
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
					},
				},
				nodeName: "node2",
			},
			want:    &NodeResources{},
			wantErr: false,
		},
		{
			name: "Safely dereference pointers",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
							resources := api.V0044NodeResourceLayoutListObject{
								{Node: "node1"},
								{Node: "node2"},
							}
							if o, ok := obj.(*api.V0044NodeResourceLayoutListObject); ok {
								layout := resources.DeepCopy()
								*o = *layout
							}
							return nil
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
					},
				},
				nodeName: "node2",
			},
			want: &NodeResources{
				Node: "node2",
			},
			wantErr: false,
		},
		{
			name: "Return GRES and node Extra",
			fields: fields{
				Client: func() client.Client {
					f := interceptor.Funcs{
						Get: func(ctx context.Context, key object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
							resources := api.V0044NodeResourceLayoutListObject{
								{Node: "node1"},
								{
									Node: "node2",
									Gres: &api.V0044NodeGresLayoutList{
										{
											Count: ptr.To(int64(2)),
											Index: ptr.To("1-2"),
											Name:  "gpu",
											Type:  ptr.To("gpu.example.com"),
										},
									},
								},
							}
							if o, ok := obj.(*api.V0044NodeResourceLayoutListObject); ok {
								layout := resources.DeepCopy()
								*o = *layout
							}
							if o, ok := obj.(*api.V0044Node); ok {
								o.Extra = ptr.To("slurm-bridge.dra-gres-map={}")
							}
							return nil
						},
					}
					return fake.NewClientBuilder().
						WithInterceptorFuncs(f).
						Build()
				}(),
			},
			args: args{
				ctx: context.Background(),
				pod: &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Labels: map[string]string{wellknown.LabelExternalJobId: "1"},
					},
				},
				nodeName: "node2",
			},
			want: &NodeResources{
				Node:      "node2",
				NodeExtra: "slurm-bridge.dra-gres-map={}",
				Gres: []GresLayout{
					{
						Count: int64(2),
						Index: "1-2",
						Name:  "gpu",
						Type:  "gpu.example.com",
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &realSlurmControl{
				lastUpdate: lru.New(10),
				Client:     tt.fields.Client,
				mcsLabel:   tt.fields.mcsLabel,
				partition:  tt.fields.partition,
			}
			got, err := r.GetResources(tt.args.ctx, tt.args.pod, tt.args.nodeName)
			if (err != nil) != tt.wantErr {
				t.Errorf("realSlurmControl.GetResources() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("realSlurmControl.GetResources() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_realSlurmControl_UpdateJobSkipsUnchanged(t *testing.T) {
	ctx := context.Background()
	pod := st.MakePod().Name("foo").Namespace("slurm-bridge").
		Labels(map[string]string{wellknown.LabelExternalJobId: "7"}).Obj()
	jobIR := func(exc ...string) *slurmjobir.SlurmJobIR {
		return &slurmjobir.SlurmJobIR{Components: []slurmjobir.SlurmJobComponent{{
			JobInfo: slurmjobir.SlurmJobIRJobInfo{ExcNodes: exc},
			Pods:    corev1.PodList{Items: []corev1.Pod{*pod.DeepCopy()}},
		}}}
	}
	updates := 0
	failUpdate := false
	r := &realSlurmControl{lastUpdate: lru.New(10), Client: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Update: func(_ context.Context, _ object.Object, _ any, opts ...client.UpdateOption) error {
			updates++
			if !(&client.UpdateOptions{}).ApplyOptions(opts).SkipReadAfterUpdate {
				t.Fatal("eligibility update must not wait for readback")
			}
			if failUpdate {
				return errors.New("update applied but response lost")
			}
			return nil
		},
		Delete: func(context.Context, object.Object, ...client.DeleteOption) error { return nil },
	}).Build()}

	for i, step := range []struct {
		ir     *slurmjobir.SlurmJobIR
		delete bool
		fail   bool
		want   int
	}{
		{ir: jobIR(), want: 1},
		{ir: jobIR(), want: 1},
		{ir: jobIR("node2"), want: 2},
		{ir: jobIR("node2"), delete: true, want: 3},
		// A failed update may still have reached Slurm, so the next request is resent.
		{ir: jobIR(), fail: true, want: 4},
		{ir: jobIR("node2"), want: 5},
	} {
		if step.delete {
			if err := r.DeleteJob(ctx, pod); err != nil {
				t.Fatal(err)
			}
		}
		failUpdate = step.fail
		if _, err := r.UpdateJob(ctx, pod, step.ir); (err != nil) != step.fail {
			t.Fatalf("step %d: err = %v, want failure %v", i, err, step.fail)
		}
		if updates != step.want {
			t.Fatalf("step %d: updates = %d, want %d", i, updates, step.want)
		}
	}
}

func Test_realSlurmControl_ReleaseJob(t *testing.T) {
	writeErr := errors.New("update failed")
	for _, tt := range []struct {
		name       string
		priority   *int32
		failAt     int
		wantWrites int
	}{
		{name: "Slurm priority", wantWrites: 1},
		{name: "explicit priority", priority: ptr.To(int32(500)), wantWrites: 2},
		{name: "zero priority", priority: ptr.To(int32(0)), wantWrites: 2},
		{name: "release fails", priority: ptr.To(int32(500)), failAt: 1, wantWrites: 1},
		{name: "priority fails", priority: ptr.To(int32(500)), failAt: 2, wantWrites: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writes := 0
			r := &realSlurmControl{Client: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, obj object.Object, req any, opts ...client.UpdateOption) error {
					writes++
					if obj.GetKey() != "42" {
						t.Fatalf("updated job = %s, want 42", obj.GetKey())
					}
					if !(&client.UpdateOptions{}).ApplyOptions(opts).SkipReadAfterUpdate {
						t.Fatal("hold release and priority updates must not wait for readback")
					}
					want := api.V0044JobDescMsg{Hold: ptr.To(false)}
					if writes == 2 {
						want = api.V0044JobDescMsg{Priority: &api.V0044Uint32NoValStruct{
							Set: ptr.To(true), Number: tt.priority, Infinite: ptr.To(false),
						}}
					}
					if !apiequality.Semantic.DeepEqual(req, want) {
						t.Errorf("write %d = %#v, want %#v", writes, req, want)
					}
					if writes == tt.failAt {
						return writeErr
					}
					// Leave obj unchanged, as SkipReadAfterUpdate does.
					return nil
				},
				Get: func(context.Context, object.ObjectKey, object.Object, ...client.GetOption) error {
					t.Fatal("release must not read the job back")
					return nil
				},
			}).Build()}
			err := r.ReleaseJob(t.Context(), 42, tt.priority)
			if tt.failAt == 0 && err != nil || tt.failAt != 0 && !errors.Is(err, writeErr) {
				t.Fatalf("ReleaseJob() = %v, failure expected: %t", err, tt.failAt != 0)
			}
			if writes != tt.wantWrites {
				t.Errorf("writes = %d, want %d", writes, tt.wantWrites)
			}
		})
	}
}
