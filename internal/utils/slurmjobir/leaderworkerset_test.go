// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func newLWS(name string, size int32) *lwsv1.LeaderWorkerSet {
	return &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       metav1.NamespaceDefault,
			Name:            name,
			ResourceVersion: "999",
		},
		Spec: lwsv1.LeaderWorkerSetSpec{
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size: ptr.To(size),
			},
		},
	}
}

func newLWSPod(name, groupHash string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      name,
			Labels: map[string]string{
				lwsv1.GroupUniqueHashLabelKey: groupHash,
			},
			ResourceVersion: "999",
		},
	}
}

func newLWSRolePod(name, groupHash, workerIndex string) *corev1.Pod {
	pod := newLWSPod(name, groupHash)
	pod.Labels[lwsv1.WorkerIndexLabelKey] = workerIndex
	return pod
}

func Test_translator_PreFilterLWS(t *testing.T) {
	type fields struct {
		Reader client.Reader
		ctx    context.Context
	}
	type args struct {
		pod        *corev1.Pod
		slurmJobIR *SlurmJobIR
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   *fwk.Status
	}{
		{
			name: "Fail to get LWS",
			fields: fields{
				Reader: fake.NewFakeClient(),
				ctx:    context.Background(),
			},
			args: args{
				pod:        &corev1.Pod{},
				slurmJobIR: &SlurmJobIR{},
			},
			want: fwk.NewStatus(fwk.Error, ErrorLWSCouldNotGet.Error()),
		},
		{
			name: "Not enough pods for group",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "lws",
							Namespace: corev1.NamespaceDefault,
						},
					},
					Components: []SlurmJobComponent{
						{
							Pods: corev1.PodList{},
						},
					},
				},
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
			},
			want: fwk.NewStatus(fwk.Unschedulable, ErrorInsuffientPods.Error()),
		},
		{
			name: "Invalid state with external job and insufficient pods",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "lws",
							Namespace: corev1.NamespaceDefault,
						},
					},
					Components: []SlurmJobComponent{
						{
							Pods: corev1.PodList{},
						},
					},
				},
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
							wellknown.LabelExternalJobId:  "1",
						},
					},
				},
			},
			want: fwk.NewStatus(fwk.Unschedulable, ErrorExternalJobInvalid.Error()),
		},
		{
			name: "group has enough pods",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				slurmJobIR: &SlurmJobIR{
					RootPOM: metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "lws",
							Namespace: corev1.NamespaceDefault,
						},
					},
					Components: []SlurmJobComponent{
						{
							Pods: corev1.PodList{
								Items: []corev1.Pod{
									*newLWSPod("pod1", "lws"),
									*newLWSPod("pod2", "lws"),
								},
							},
						},
					},
				},
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
			},
			want: fwk.NewStatus(fwk.Success),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &translator{
				Reader: tt.fields.Reader,
				ctx:    tt.fields.ctx,
			}
			got := tr.PreFilterLWS(tt.args.pod, tt.args.slurmJobIR)
			if !got.Equal(tt.want) {
				t.Errorf("translator.PreFilterLWS() = %v, want %v", got.AsError(), tt.want.AsError())
			}
		})
	}
}

func Test_translator_fromLws(t *testing.T) {
	type fields struct {
		Reader client.Reader
		ctx    context.Context
	}
	type args struct {
		pod     *corev1.Pod
		rootPOM *metav1.PartialObjectMetadata
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *SlurmJobIR
		wantErr bool
	}{
		{
			name: "Fail to get LWS",
			fields: fields{
				Reader: fake.NewFakeClient(),
				ctx:    context.Background(),
			},
			args: args{
				pod: &corev1.Pod{},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Could not list pods",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							return errors.New("Failed to List")
						},
					}).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "No LWS pods found",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(corev1.AddToScheme(scheme))
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
						},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "LWS to flat SlurmJobIR without opt-in",
			fields: fields{
				Reader: func() client.Client {
					scheme := runtime.NewScheme()
					utilruntime.Must(corev1.AddToScheme(scheme))
					utilruntime.Must(lwsv1.AddToScheme(scheme))
					return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
						newLWS("lws", 2),
						newLWSRolePod("pod1", "lws", "0"),
						newLWSRolePod("pod2", "lws", "1"),
					).Build()
				}(),
				ctx: context.Background(),
			},
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							lwsv1.GroupUniqueHashLabelKey: "lws",
							lwsv1.SetNameLabelKey:         "foo",
							lwsv1.GroupIndexLabelKey:      "1",
							lwsv1.WorkerIndexLabelKey:     "0",
						},
					},
				},
				rootPOM: &metav1.PartialObjectMetadata{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lws",
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want: &SlurmJobIR{
				Components: []SlurmJobComponent{
					{
						ObjectMeta: metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
							Name: "lws", Namespace: metav1.NamespaceDefault,
						}},
						JobInfo: SlurmJobIRJobInfo{
							JobName:      ptr.To("foo-1"),
							MaxNodes:     ptr.To(int32(2)),
							MinNodes:     ptr.To(int32(2)),
							TasksPerNode: ptr.To(int32(1)),
						},
						Pods: corev1.PodList{
							Items: []corev1.Pod{
								*newLWSRolePod("pod1", "lws", "0"),
								*newLWSRolePod("pod2", "lws", "1"),
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &translator{
				Reader: tt.fields.Reader,
				ctx:    tt.fields.ctx,
			}
			got, err := tr.fromLws(tt.args.pod, tt.args.rootPOM)
			if (err != nil) != tt.wantErr {
				t.Errorf("translator.fromLws() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("translator.fromLws() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Exercise the StatefulSet ownership chains used by ordinal LWS controllers,
// including manifests created before groupIdentity was introduced.
func TestTranslateOrdinalLWSReplicaGroups(t *testing.T) {
	for _, identity := range []struct {
		name  string
		value lwsv1.GroupIdentityType
	}{
		{name: "default"},
		{name: "explicit ordinal", value: lwsv1.GroupIdentityOrdinal},
	} {
		t.Run(identity.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			utilruntime.Must(corev1.AddToScheme(scheme))
			utilruntime.Must(appsv1.AddToScheme(scheme))
			utilruntime.Must(lwsv1.AddToScheme(scheme))

			lws := newLWS("lws", 2)
			lws.UID = "lws-uid"
			lws.Spec.Replicas = ptr.To[int32](2)
			lws.Spec.GroupIdentity = identity.value
			lws.Annotations = map[string]string{wellknown.AnnotationPartition: "lws-partition"}
			leaders := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
				Name: "lws", Namespace: lws.Namespace, UID: "leaders-uid",
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(lws, lwsv1.GroupVersion.WithKind("LeaderWorkerSet"))},
			}}
			objects := []client.Object{lws, leaders}
			groups := map[string][]*corev1.Pod{}
			for _, index := range []string{"0", "1"} {
				leaderName := "lws-" + index
				newPod := func(name, workerIndex string, owner *appsv1.StatefulSet) *corev1.Pod {
					return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
						Name: name, Namespace: lws.Namespace, UID: types.UID(name + "-uid"),
						// Literal wire labels detect upstream label changes during upgrades.
						Labels: map[string]string{
							"leaderworkerset.sigs.k8s.io/name":         lws.Name,
							"leaderworkerset.sigs.k8s.io/group-index":  index,
							"leaderworkerset.sigs.k8s.io/group-key":    "group-" + index,
							"leaderworkerset.sigs.k8s.io/worker-index": workerIndex,
						},
						OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(owner, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
					}}
				}
				leader := newPod(leaderName, "0", leaders)
				workers := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
					Name: leaderName, Namespace: lws.Namespace, UID: types.UID(leaderName + "-workers-uid"),
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(leader, corev1.SchemeGroupVersion.WithKind("Pod"))},
				}}
				worker := newPod(leaderName+"-1", "1", workers)
				groups[index] = []*corev1.Pod{leader, worker}
				objects = append(objects, leader, workers, worker)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			for _, index := range []string{"0", "1"} {
				for _, pod := range groups[index] {
					t.Run(pod.Name, func(t *testing.T) {
						ir, err := TranslateToSlurmJobIR(c, nil, nil, context.Background(), pod)
						if err != nil {
							t.Fatal(err)
						}
						if ir.RootPOM.TypeMeta != lws_v1 || ir.RootPOM.Name != lws.Name {
							t.Fatalf("scheduling root = %s %s, want LeaderWorkerSet %s", ir.RootPOM.Kind, ir.RootPOM.Name, lws.Name)
						}
						if len(ir.Components) != 1 || len(ir.AllPods()) != 2 {
							t.Fatalf("replica allocation has %d components and %d Pods, want 1 and 2", len(ir.Components), len(ir.AllPods()))
						}
						for _, member := range ir.AllPods() {
							if member.Labels["leaderworkerset.sigs.k8s.io/group-index"] != index {
								t.Errorf("replica %s contains Pod %s from another replica", index, member.Name)
							}
						}
						info := ir.Components[0].JobInfo
						if ptr.Deref(info.JobName, "") != "lws-"+index || ptr.Deref(info.Partition, "") != "lws-partition" {
							t.Errorf("job name or LWS annotations changed: %+v", info)
						}
						if ptr.Deref(info.MinNodes, 0) != 2 || ptr.Deref(info.MaxNodes, 0) != 2 || ptr.Deref(info.TasksPerNode, 0) != 1 {
							t.Errorf("replica allocation sizing changed: %+v", info)
						}
						tr := &translator{Reader: c, ctx: context.Background()}
						if status := tr.PreFilterLWS(pod, ir); !status.IsSuccess() {
							t.Fatalf("complete replica was rejected: %v", status.AsError())
						}
					})
				}
			}
		})
	}
}
