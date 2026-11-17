// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResourceAliases(t *testing.T) {
	ctx := context.Background()
	class := func(name string, timestamp int64) *resourcev1.DeviceClass {
		return &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(time.Unix(timestamp, 0))},
			Spec:       resourcev1.DeviceClassSpec{ExtendedResourceName: ptr.To("google.com/tpu"), Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "dra.cpu"`}}}},
		}
	}
	pod := func(names ...string) *corev1.Pod {
		requests := corev1.ResourceList{}
		for _, name := range names {
			requests[corev1.ResourceName(name)] = resource.MustParse("4")
		}
		return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Requests: requests}}}}}
	}
	for _, tc := range []struct {
		name    string
		classes []client.Object
		pod     *corev1.Pod
		want    string
		wantErr bool
	}{
		{name: "alias", classes: []client.Object{class("tpu", 1)}, pod: pod("google.com/tpu"), want: "tpu"},
		{name: "newest class", classes: []client.Object{class("old", 1), class("new", 2)}, pod: pod("google.com/tpu"), want: "new"},
		{name: "name breaks tie", classes: []client.Object{class("z", 1), class("a", 1)}, pod: pod("google.com/tpu"), want: "a"},
		{name: "unclaimed plugin name", pod: pod("google.com/tpu")},
		{name: "canonical and alias", classes: []client.Object{class("tpu", 1)}, pod: pod("google.com/tpu", resourcev1.ResourceDeviceClassPrefix+"tpu"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, err := DefaultRegistry().ResolvePod(ctx, fake.NewClientBuilder().WithObjects(tc.classes...).Build(), tc.pod)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "use one resource name") {
					t.Fatalf("ResolvePod() = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if len(requests) > 0 {
				got = requests[0].DeviceClassName
			}
			if got != tc.want {
				t.Fatalf("alias = %q; want %q", got, tc.want)
			}
		})
	}
	t.Run("init container limits", func(t *testing.T) {
		p := &corev1.Pod{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "init", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"google.com/tpu": resource.MustParse("4")}}}}}}
		requests, err := DefaultRegistry().ResolvePod(ctx, fake.NewClientBuilder().WithObjects(class("tpu", 1)).Build(), p)
		if err != nil || len(requests) != 1 || requests[0].DeviceClassName != "tpu" || requests[0].ContainerName != "init" || requests[0].Count != 4 {
			t.Fatalf("ResolvePod() = %v, %v", requests, err)
		}
	})
	t.Run("lookup failure is not a device plugin fallback", func(t *testing.T) {
		r := DefaultRegistry()
		if _, err := r.ResolvePod(ctx, failingAliasReader{}, pod("google.com/tpu")); err == nil {
			t.Fatal("expected lookup failure")
		}
		if _, err := r.ResolvePod(ctx, failingAliasReader{}, pod("cpu")); err != nil {
			t.Fatalf("native resources should not list classes: %v", err)
		}
	})
}

type failingAliasReader struct{ client.Reader }

func (failingAliasReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("unavailable")
}

func TestDeviceClassIndexLifecycle(t *testing.T) {
	registry := DefaultRegistry()
	informer := &classTestInformer{}
	if err := registry.WatchDeviceClasses(informer); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"example.com/cpu": resource.MustParse("4")}}}}}}
	if _, err := registry.ResolvePod(t.Context(), failingAliasReader{}, pod); err == nil {
		t.Fatal("unsynced index must not fall back to a device plugin")
	}
	class := func(name string, timestamp int64) *resourcev1.DeviceClass {
		return &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(time.Unix(timestamp, 0))}, Spec: resourcev1.DeviceClassSpec{
			ExtendedResourceName: ptr.To("example.com/cpu"), Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "dra.cpu"`}}},
		}}
	}
	old, newest := class("old", 1), class("new", 2)
	informer.handler.OnAdd(old, true)
	informer.synced = true
	check := func(want string) {
		t.Helper()
		requests, err := registry.ResolvePod(t.Context(), failingAliasReader{}, pod)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if len(requests) > 0 {
			got = requests[0].DeviceClassName
		}
		if got != want {
			t.Fatalf("class = %q, want %q", got, want)
		}
	}
	check("old")
	informer.handler.OnAdd(newest, false)
	check("new")
	informer.handler.OnDelete(cache.DeletedFinalStateUnknown{Obj: old})
	check("new")
	informer.handler.OnAdd(old, false)
	informer.handler.OnDelete(newest)
	check("old")
	changed := old.DeepCopy()
	changed.Spec.ExtendedResourceName = ptr.To("example.com/renamed")
	informer.handler.OnUpdate(old, changed)
	check("")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{"example.com/renamed": resource.MustParse("4")}
	check("old")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "old"): resource.MustParse("4")}
	check("old")
	informer.handler.OnDelete(changed)
	if _, err := registry.ResolvePod(t.Context(), failingAliasReader{}, pod); err == nil {
		t.Fatal("deleted canonical class must fail")
	}
}

type classTestInformer struct {
	cache.ResourceEventHandlerRegistration
	handler cache.ResourceEventHandler
	synced  bool
}

func (i *classTestInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	i.handler = handler
	return i, nil
}
func (i *classTestInformer) HasSynced() bool { return i.synced }
