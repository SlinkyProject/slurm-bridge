// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha2 "k8s.io/api/scheduling/v1alpha2"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type kubeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f kubeRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestKubeClientContentNegotiation(t *testing.T) {
	for _, version := range []string{"", slurmjobir.WorkloadAPIVersionV1Alpha2, slurmjobir.WorkloadAPIVersionV1Beta1} {
		name := version
		if name == "" {
			name = "without-workload-api"
		}
		t.Run(name, func(t *testing.T) {
			scheme, err := newClientScheme()
			if err != nil {
				t.Fatal(err)
			}
			var workloadAPI *slurmjobir.WorkloadAPI
			if version != "" {
				workloadAPI = mustRegisterTestWorkloadAPI(t, scheme, version)
			}
			const namespace = "default"
			pod := &corev1.Pod{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace, Name: "worker",
					Labels:      map[string]string{wellknown.LabelExternalJobId: "5"},
					Annotations: map[string]string{wellknown.AnnotationExternalJobNode: "node-a"},
				},
				Spec: corev1.PodSpec{SchedulingGroup: &corev1.PodSchedulingGroup{PodGroupName: ptr.To("group")}},
			}
			groupVersion := "scheduling.k8s.io/" + version
			pg := &slurmjobir.PodGroup{
				TypeMeta:   metav1.TypeMeta{APIVersion: groupVersion, Kind: "PodGroup"},
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "group", Generation: 1},
				Spec: slurmjobir.PodGroupSpec{
					SchedulingPolicy: schedulingv1alpha2.PodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha2.GangSchedulingPolicy{MinCount: 1},
					},
				},
			}
			if version == slurmjobir.WorkloadAPIVersionV1Alpha2 {
				pg.Spec.PodGroupTemplateRef = &schedulingv1alpha2.PodGroupTemplateReference{
					Workload: &schedulingv1alpha2.WorkloadPodGroupTemplateReference{WorkloadName: "workload"},
				}
			} else {
				pg.Spec.WorkloadRef = &slurmjobir.WorkloadReference{WorkloadName: "workload"}
			}
			pgPath := "/apis/" + groupVersion + "/namespaces/" + namespace + "/podgroups/group"
			workloadPath := "/apis/" + groupVersion + "/namespaces/" + namespace + "/workloads/workload"
			metadataType := metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"}
			objects := map[string]runtime.Object{
				"/api/v1/namespaces/default/pods/worker": pod,
				"/api/v1/namespaces/default/pods": &corev1.PodList{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{*pod},
				},
				"/api/v1/nodes": &corev1.NodeList{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"},
					Items:    []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}},
				},
				workloadPath: &metav1.PartialObjectMetadata{
					TypeMeta: metadataType,
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace, Name: "workload",
						Annotations: map[string]string{wellknown.AnnotationQOS: "workload-qos"},
					},
				},
			}
			discovery := map[string]any{
				"/api":  &metav1.APIVersions{Versions: []string{"v1"}},
				"/apis": &metav1.APIGroupList{},
				"/api/v1": &metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
					{Name: "pods", Kind: "Pod", Namespaced: true},
					{Name: "nodes", Kind: "Node"},
				}},
				"/apis/" + groupVersion: &metav1.APIResourceList{GroupVersion: groupVersion, APIResources: []metav1.APIResource{
					{Name: "podgroups", Kind: "PodGroup", Namespaced: true},
					{Name: "workloads", Kind: "Workload", Namespaced: true},
				}},
			}
			protobuf, ok := runtime.SerializerInfoForMediaType(serializer.NewCodecFactory(scheme).SupportedMediaTypes(), runtime.ContentTypeProtobuf)
			if !ok {
				t.Fatal("protobuf serializer unavailable")
			}
			podGroupGets, statusPatches, workloadGets, podStatusPatches := 0, 0, 0, 0
			transport := kubeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				var data []byte
				var err error
				contentType := runtime.ContentTypeJSON
				accept := req.Header.Get("Accept")
				if discoveryObject, ok := discovery[req.URL.Path]; ok {
					data, err = json.Marshal(discoveryObject)
				} else if (req.URL.Path == pgPath || req.URL.Path == pgPath+"/status") && !strings.Contains(accept, "as=PartialObjectMetadata") {
					if accept != runtime.ContentTypeJSON {
						t.Errorf("PodGroup %s Accept = %q, want JSON only", req.Method, accept)
					}
					data, err = json.Marshal(pg)
					if req.Method == http.MethodPatch && err == nil {
						statusPatches++
						if req.URL.Path != pgPath+"/status" || req.Header.Get("Content-Type") != string(client.StrategicMergeFrom(pg).Type()) {
							t.Errorf("unexpected PodGroup patch: %s, Content-Type %q", req.URL.Path, req.Header.Get("Content-Type"))
						}
						patch, readErr := io.ReadAll(req.Body)
						if readErr != nil {
							return nil, readErr
						}
						data, err = strategicpatch.StrategicMergePatch(data, patch, slurmjobir.PodGroup{})
						if err == nil {
							err = json.Unmarshal(data, pg)
						}
					} else if req.Method == http.MethodGet {
						podGroupGets++
					}
				} else {
					if !strings.HasPrefix(accept, runtime.ContentTypeProtobuf) {
						t.Errorf("%s Accept = %q, want protobuf preferred", req.URL.Path, accept)
					}
					obj := objects[req.URL.Path]
					if req.URL.Path == "/api/v1/namespaces/default/pods/worker/status" && req.Method == http.MethodPatch {
						podStatusPatches++
						patch, readErr := io.ReadAll(req.Body)
						if readErr != nil {
							return nil, readErr
						}
						if err := json.Unmarshal(patch, pod); err != nil {
							return nil, err
						}
						obj = pod
					}
					if req.URL.Path == pgPath {
						obj = &metav1.PartialObjectMetadata{TypeMeta: metadataType, ObjectMeta: pg.ObjectMeta}
					}
					if obj == nil {
						return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
					}
					if req.URL.Path == workloadPath {
						workloadGets++
					}
					contentType = runtime.ContentTypeProtobuf
					data, err = runtime.Encode(protobuf.Serializer, obj)
				}
				if err != nil {
					return nil, err
				}
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}},
					Body: io.NopCloser(bytes.NewReader(data)), Request: req,
				}, nil
			})
			config := &rest.Config{
				Host: "https://kubernetes.test", Transport: transport,
				ContentConfig: rest.ContentConfig{ContentType: runtime.ContentTypeProtobuf},
			}
			originalContentConfig := config.ContentConfig
			kubeClient, err := newKubeClient(config, scheme)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(config.ContentConfig, originalContentConfig) {
				t.Fatal("client construction changed the scheduler content configuration")
			}
			if kubeClient.RESTMapper() != kubeClient.(*podGroupJSONClient).jsonClient.RESTMapper() {
				t.Fatal("clients do not share their REST mapper")
			}
			ctx := context.Background()
			var fetchedPod corev1.Pod
			if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(pod), &fetchedPod); err != nil {
				t.Fatalf("Get Pod: %v", err)
			}
			var nodes corev1.NodeList
			if err := kubeClient.List(ctx, &nodes); err != nil {
				t.Fatalf("List Nodes: %v", err)
			}
			if fetchedPod.Name != pod.Name || len(nodes.Items) != 1 || nodes.Items[0].Name != "node-a" {
				t.Fatal("protobuf responses were not decoded correctly")
			}
			originalPod := fetchedPod.DeepCopy()
			fetchedPod.Status.Phase = corev1.PodRunning
			if err := kubeClient.Status().Patch(ctx, &fetchedPod, client.MergeFrom(originalPod)); err != nil {
				t.Fatalf("Patch Pod status: %v", err)
			}
			if podStatusPatches != 1 || pod.Status.Phase != corev1.PodRunning || fetchedPod.Status.Phase != corev1.PodRunning {
				t.Fatal("Pod status patch did not round-trip through protobuf")
			}
			if workloadAPI == nil {
				return
			}
			sb := &SlurmBridge{Client: kubeClient, workloadAPI: workloadAPI, schedulerName: "slurm-bridge"}
			ir, err := slurmjobir.TranslateToSlurmJobIR(sb.Client, dra.DefaultRegistry(), workloadAPI, ctx, pod)
			if err != nil {
				t.Fatalf("TranslateToSlurmJobIR: %v", err)
			}
			if len(ir.Pods.Items) != 1 || ptr.Deref(ir.JobInfo.QOS, "") != "workload-qos" {
				t.Fatalf("unexpected translation: %#v", ir)
			}
			if status := slurmjobir.PreFilter(sb.Client, dra.DefaultRegistry(), workloadAPI, ctx, pod, ir); !status.IsSuccess() {
				t.Fatalf("PreFilter: %v", status)
			}
			sb.markPodGroupScheduled(ctx, ir, "5")
			if condition := apimeta.FindStatusCondition(pg.Status.Conditions, workloadAPI.ScheduledCondition); condition == nil || condition.Status != metav1.ConditionTrue {
				t.Fatalf("scheduled condition = %#v, want True", condition)
			}
			if podGroupGets != 3 || statusPatches != 1 || workloadGets != 1 {
				t.Fatalf("requests: PodGroup GETs=%d, status PATCHes=%d, Workload metadata GETs=%d; want 3, 1, 1", podGroupGets, statusPatches, workloadGets)
			}
		})
	}
}
