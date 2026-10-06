// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package workloadclient adapts the embedded scheduler's v1beta1 PodGroup
// reads, watches and status writes to the API version served by the cluster.
// Spec creation and replacement are deliberately outside this adapter: the
// typed representation cannot preserve all fields of another API version.
package workloadclient

import (
	"context"
	"encoding/json"
	"fmt"

	scheduling "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	apply "k8s.io/client-go/applyconfigurations/scheduling/v1beta1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	typed "k8s.io/client-go/kubernetes/typed/scheduling/v1beta1"
	"k8s.io/client-go/rest"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

const alphaPodGroupScheduled = "PodGroupScheduled"

// New discovers the native Workload API. Other Kubernetes resources retain the
// original client, including its protobuf support.
func New(config *rest.Config, base kubernetes.Interface) (kubernetes.Interface, error) {
	api, err := slurmjobir.RegisterWorkloadAPI(base.Discovery(), runtime.NewScheme())
	if err != nil {
		return nil, err
	}
	if api.PodGroupTypeMeta.APIVersion == scheduling.SchemeGroupVersion.String() {
		return base, nil
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &clientset{Interface: base, groups: dyn.Resource(schema.GroupVersionResource{
		Group: "scheduling.k8s.io", Version: "v1alpha2", Resource: "podgroups",
	})}, nil
}

type clientset struct {
	kubernetes.Interface
	groups dynamic.NamespaceableResourceInterface
}

func (c *clientset) SchedulingV1beta1() typed.SchedulingV1beta1Interface {
	return &schedulingClient{SchedulingV1beta1Interface: c.Interface.SchedulingV1beta1(), groups: c.groups}
}

type schedulingClient struct {
	typed.SchedulingV1beta1Interface
	groups dynamic.NamespaceableResourceInterface
}

func (c *schedulingClient) PodGroups(namespace string) typed.PodGroupInterface {
	return &podGroups{resource: c.groups.Namespace(namespace)}
}

type podGroups struct {
	resource dynamic.ResourceInterface
}

func decode(obj *unstructured.Unstructured, err error) (*scheduling.PodGroup, error) {
	if err != nil {
		return nil, err
	}
	obj = obj.DeepCopy()
	translateConditionTypes(obj.Object["status"], alphaPodGroupScheduled, scheduling.PodGroupInitiallyScheduled)
	if spec, ok := obj.Object["spec"].(map[string]any); ok {
		// Beta changed the enum into a union and moved the Workload reference.
		if mode, ok := spec["disruptionMode"].(string); ok {
			switch mode {
			case "Pod":
				spec["disruptionMode"] = map[string]any{"single": map[string]any{}}
			case "PodGroup":
				spec["disruptionMode"] = map[string]any{"all": map[string]any{}}
			default:
				return nil, fmt.Errorf("unsupported alpha PodGroup disruptionMode: %v", mode)
			}
		}
		if ref, ok := spec["podGroupTemplateRef"].(map[string]any); ok {
			if workload, ok := ref["workload"].(map[string]any); ok {
				spec["workloadRef"] = map[string]any{
					"workloadName": workload["workloadName"], "templateName": workload["podGroupTemplateName"],
				}
			}
		}
	}
	out := &scheduling.PodGroup{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, out); err != nil {
		return nil, fmt.Errorf("decode alpha PodGroup %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	out.APIVersion = scheduling.SchemeGroupVersion.String()
	return out, nil
}

// Strategic merge patches also carry condition keys in $setElementOrder and
// deletion directives. Translate those keys along with the condition objects.
func translateConditionTypes(value any, from, to string) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "type" && child == from {
				value[key] = to
			} else {
				translateConditionTypes(child, from, to)
			}
		}
	case []any:
		for _, child := range value {
			translateConditionTypes(child, from, to)
		}
	}
}

func (p *podGroups) Get(ctx context.Context, name string, opts metav1.GetOptions) (*scheduling.PodGroup, error) {
	return decode(p.resource.Get(ctx, name, opts))
}

func (p *podGroups) List(ctx context.Context, opts metav1.ListOptions) (*scheduling.PodGroupList, error) {
	list, err := p.resource.List(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := &scheduling.PodGroupList{TypeMeta: metav1.TypeMeta{APIVersion: scheduling.SchemeGroupVersion.String(), Kind: "PodGroupList"}}
	out.ResourceVersion, out.Continue, out.RemainingItemCount = list.GetResourceVersion(), list.GetContinue(), list.GetRemainingItemCount()
	for i := range list.Items {
		pg, err := decode(&list.Items[i], nil)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, *pg)
	}
	return out, nil
}

func (p *podGroups) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	source, err := p.resource.Watch(ctx, opts)
	if err != nil {
		return nil, err
	}
	return watch.Filter(source, func(event watch.Event) (watch.Event, bool) {
		if event.Type == watch.Error {
			return event, true
		}
		obj, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			return watch.Event{Type: watch.Error, Object: &metav1.Status{Status: metav1.StatusFailure,
				Message: fmt.Sprintf("unexpected PodGroup watch object %T", event.Object), Code: 500}}, true
		}
		pg, err := decode(obj, nil)
		if err != nil {
			return watch.Event{Type: watch.Error, Object: &metav1.Status{Status: metav1.StatusFailure, Message: err.Error(), Code: 500}}, true
		}
		return watch.Event{Type: event.Type, Object: pg}, true
	}), nil
}

func (p *podGroups) Patch(ctx context.Context, name string, kind types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*scheduling.PodGroup, error) {
	if len(subresources) != 1 || subresources[0] != "status" || (kind != types.StrategicMergePatchType && kind != types.MergePatchType && kind != types.ApplyPatchType) {
		return nil, fmt.Errorf("PodGroup compatibility client only supports JSON status merge/apply patches")
	}
	var patch map[string]any
	if err := json.Unmarshal(data, &patch); err != nil {
		return nil, err
	}
	translateConditionTypes(patch["status"], scheduling.PodGroupInitiallyScheduled, alphaPodGroupScheduled)
	if _, ok := patch["apiVersion"]; ok {
		patch["apiVersion"] = "scheduling.k8s.io/v1alpha2"
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return decode(p.resource.Patch(ctx, name, kind, data, opts, subresources...))
}

func (p *podGroups) UpdateStatus(ctx context.Context, pg *scheduling.PodGroup, opts metav1.UpdateOptions) (*scheduling.PodGroup, error) {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pg)
	if err != nil {
		return nil, err
	}
	// PUT replaces status, including fields omitted by omitempty. The status
	// subresource preserves the stored spec, so do not send its beta wire shape.
	delete(obj, "spec")
	obj["apiVersion"], obj["kind"] = "scheduling.k8s.io/v1alpha2", "PodGroup"
	translateConditionTypes(obj["status"], scheduling.PodGroupInitiallyScheduled, alphaPodGroupScheduled)
	return decode(p.resource.UpdateStatus(ctx, &unstructured.Unstructured{Object: obj}, opts))
}

func (p *podGroups) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return p.resource.Delete(ctx, name, opts)
}

func (p *podGroups) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	return p.resource.DeleteCollection(ctx, opts, listOpts)
}

func (p *podGroups) Create(context.Context, *scheduling.PodGroup, metav1.CreateOptions) (*scheduling.PodGroup, error) {
	return nil, fmt.Errorf("PodGroup compatibility client does not create specs; use the served API")
}

func (p *podGroups) Update(context.Context, *scheduling.PodGroup, metav1.UpdateOptions) (*scheduling.PodGroup, error) {
	return nil, fmt.Errorf("PodGroup compatibility client does not replace specs; use the served API")
}

func (p *podGroups) Apply(context.Context, *apply.PodGroupApplyConfiguration, metav1.ApplyOptions) (*scheduling.PodGroup, error) {
	return nil, fmt.Errorf("PodGroup compatibility client does not apply specs; use the served API")
}

func (p *podGroups) ApplyStatus(ctx context.Context, pg *apply.PodGroupApplyConfiguration, opts metav1.ApplyOptions) (*scheduling.PodGroup, error) {
	if pg == nil || pg.Name == nil {
		return nil, fmt.Errorf("PodGroup apply requires a name")
	}
	data, err := json.Marshal(pg)
	if err != nil {
		return nil, err
	}
	return p.Patch(ctx, *pg.Name, types.ApplyPatchType, data, opts.ToPatchOptions(), "status")
}
