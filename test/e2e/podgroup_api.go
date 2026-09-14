// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

type kubernetesPodGroupAPI string

const (
	podGroupV1Alpha2 kubernetesPodGroupAPI = "v1alpha2"
	podGroupV1Beta1  kubernetesPodGroupAPI = "v1beta1"
)

type podGroupDiscovery interface {
	ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error)
	ServerVersion() (*version.Info, error)
}

func discoverKubernetesPodGroupAPI(discoveryClient podGroupDiscovery) (kubernetesPodGroupAPI, error) {
	for _, api := range []kubernetesPodGroupAPI{podGroupV1Beta1, podGroupV1Alpha2} {
		resources, err := discoveryClient.ServerResourcesForGroupVersion("scheduling.k8s.io/" + string(api))
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("discover PodGroup API %s: %w", api, err)
		}
		for _, name := range []string{"workloads", "podgroups"} {
			if resources == nil || !slices.ContainsFunc(resources.APIResources, func(resource metav1.APIResource) bool {
				return resource.Name == name
			}) {
				return "", fmt.Errorf("PodGroup API %s does not advertise %s", api, name)
			}
		}
		return api, nil
	}
	serverInfo, err := discoveryClient.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("discover Kubernetes server version: %w", err)
	}
	serverVersion, err := utilversion.ParseGeneric(serverInfo.GitVersion)
	if err != nil {
		return "", fmt.Errorf("parse Kubernetes server version: %w", err)
	}
	if serverVersion.Major() == 1 && serverVersion.Minor() == 35 {
		return "", nil
	}
	return "", fmt.Errorf("cluster running Kubernetes %s does not serve a supported Workload and PodGroup API", serverVersion)
}

func requireKubernetesPodGroupAPI(t *testing.T, config *envconf.Config, api kubernetesPodGroupAPI) {
	t.Helper()
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config.Client().RESTConfig())
	if err != nil {
		t.Fatalf("create Kubernetes discovery client: %v", err)
	}
	selected, err := discoverKubernetesPodGroupAPI(discoveryClient)
	if err != nil {
		t.Fatal(err)
	}
	if selected == "" {
		t.Skip("Kubernetes 1.35 serves the unsupported v1alpha1 PodGroup API")
	}
	if selected != api {
		t.Skipf("cluster uses the %s PodGroup API; this case tests %s", selected, api)
	}
}

// Use complete API objects independently of the scheduler's partial wire types.
// Unstructured fixtures allow beta coverage without upgrading the Go dependencies.
func newKubernetesWorkload(api kubernetesPodGroupAPI, name, controllerGroup, controllerKind, controllerName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "scheduling.k8s.io/" + string(api),
		"kind":       "Workload",
		"metadata":   map[string]any{"name": name, "namespace": slurmBridgeNamespace},
		"spec": map[string]any{
			"controllerRef": map[string]any{"apiGroup": controllerGroup, "kind": controllerKind, "name": controllerName},
			"podGroupTemplates": []any{map[string]any{
				"name":             "workers",
				"schedulingPolicy": map[string]any{"gang": map[string]any{"minCount": int64(2)}},
			}},
		},
	}}
}

func newKubernetesPodGroup(api kubernetesPodGroupAPI, name, workloadName string) *unstructured.Unstructured {
	spec := map[string]any{
		"schedulingPolicy": map[string]any{"gang": map[string]any{"minCount": int64(2)}},
	}
	if api == podGroupV1Alpha2 {
		spec["podGroupTemplateRef"] = map[string]any{
			"workload": map[string]any{"workloadName": workloadName, "podGroupTemplateName": "workers"},
		}
	} else {
		spec["workloadRef"] = map[string]any{"workloadName": workloadName, "templateName": "workers"}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "scheduling.k8s.io/" + string(api),
		"kind":       "PodGroup",
		"metadata":   map[string]any{"name": name, "namespace": slurmBridgeNamespace},
		"spec":       spec,
	}}
}

func kubernetesPodGroupScheduled(podGroup *unstructured.Unstructured) (bool, error) {
	var observed struct {
		Status struct {
			Conditions []metav1.Condition `json:"conditions"`
		} `json:"status"`
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(podGroup.Object, &observed); err != nil {
		return false, err
	}
	version := strings.TrimPrefix(podGroup.GetAPIVersion(), "scheduling.k8s.io/")
	conditionType, err := slurmjobir.ScheduledConditionForVersion(version)
	if err != nil {
		return false, err
	}
	for _, condition := range observed.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == metav1.ConditionTrue, nil
		}
	}
	return false, nil
}
