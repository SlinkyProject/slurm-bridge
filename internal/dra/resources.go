// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResourceRequest retains the Pod's resource name alongside its resolved class.
type ResourceRequest struct {
	ResourceName    corev1.ResourceName
	ContainerName   string
	DeviceClassName string
	Profile         DeviceProfile
	Count           int64
}

type classIndex struct {
	sync.RWMutex
	classes   map[string]*resourcev1.DeviceClass
	resources map[corev1.ResourceName]*resourcev1.DeviceClass
	handler   cache.ResourceEventHandlerRegistration
}

// WatchDeviceClasses maintains a resource-name index using an existing informer.
// Call before starting admission or scheduling workers.
func (r *Registry) WatchDeviceClasses(informer interface {
	AddEventHandler(cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error)
}) error {
	handler, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.updateClass(obj, false) },
		UpdateFunc: func(_, obj any) { r.updateClass(obj, false) },
		DeleteFunc: func(obj any) { r.updateClass(obj, true) },
	})
	if err != nil {
		return err
	}
	r.classes.Lock()
	r.classes.handler = handler
	r.classes.Unlock()
	return nil
}

func (r *Registry) updateClass(obj any, deleted bool) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	class, ok := obj.(*resourcev1.DeviceClass)
	if !ok {
		return
	}
	r.classes.Lock()
	defer r.classes.Unlock()
	if r.classes.classes == nil {
		r.classes.classes = make(map[string]*resourcev1.DeviceClass)
	}
	if deleted {
		delete(r.classes.classes, class.Name)
	} else {
		r.classes.classes[class.Name] = class
	}
	// Publish a new map so an in-flight Pod keeps a consistent view. Rebuilding
	// also restores the previous winner when a colliding alias is removed.
	r.classes.resources = indexClasses(slices.Collect(maps.Values(r.classes.classes)))
}

func indexClasses(classes []*resourcev1.DeviceClass) map[corev1.ResourceName]*resourcev1.DeviceClass {
	resources := make(map[corev1.ResourceName]*resourcev1.DeviceClass, 2*len(classes))
	for _, class := range classes {
		resources[corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix+class.Name)] = class
		if class.Spec.ExtendedResourceName == nil || *class.Spec.ExtendedResourceName == "" {
			continue
		}
		name := corev1.ResourceName(*class.Spec.ExtendedResourceName)
		previous := resources[name]
		// Kubernetes gives the newest class precedence, then the first name alphabetically.
		if previous == nil || class.CreationTimestamp.After(previous.CreationTimestamp.Time) ||
			(class.CreationTimestamp.Equal(&previous.CreationTimestamp) && class.Name < previous.Name) {
			resources[name] = class
		}
	}
	return resources
}

func (r *Registry) resourceClasses(ctx context.Context, reader client.Reader) (map[corev1.ResourceName]*resourcev1.DeviceClass, error) {
	r.classes.RLock()
	handler, resources := r.classes.handler, r.classes.resources
	synced := handler == nil || handler.HasSynced()
	r.classes.RUnlock()
	if handler != nil {
		if !synced {
			return nil, fmt.Errorf("DeviceClass index has not synced")
		}
		return resources, nil
	}
	// Readers without an informer (including standalone translation) use a fresh
	// snapshot. Production workers install the watcher during setup.
	classes := &resourcev1.DeviceClassList{}
	if err := reader.List(ctx, classes); err != nil {
		return nil, fmt.Errorf("list DeviceClasses: %w", err)
	}
	items := make([]*resourcev1.DeviceClass, len(classes.Items))
	for i := range classes.Items {
		items[i] = &classes.Items[i]
	}
	return indexClasses(items), nil
}

// ResolvePod resolves each DRA request once, preserving container and resource
// names for ResourceClaim mappings. Unclaimed names remain device-plugin resources.
func (r *Registry) ResolvePod(ctx context.Context, reader client.Reader, pod *corev1.Pod) ([]ResourceRequest, error) {
	var resources map[corev1.ResourceName]*resourcev1.DeviceClass
	loaded := false
	var requests []ResourceRequest
	seen := make(map[string]ResourceRequest)
	for _, container := range slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers) {
		names := maps.Clone(container.Resources.Limits)
		if names == nil {
			names = make(corev1.ResourceList)
		}
		maps.Copy(names, container.Resources.Requests)
		for _, name := range slices.Sorted(maps.Keys(names)) {
			if !strings.Contains(string(name), "/") {
				continue
			}
			if !loaded {
				var err error
				resources, err = r.resourceClasses(ctx, reader)
				if err != nil {
					return nil, err
				}
				loaded = true
			}
			class := resources[name]
			if class == nil {
				if canonical, ok := strings.CutPrefix(string(name), resourcev1.ResourceDeviceClassPrefix); ok {
					return nil, fmt.Errorf("DeviceClass %q was not found", canonical)
				}
				continue
			}
			for field, list := range map[string]corev1.ResourceList{"request": container.Resources.Requests, "limit": container.Resources.Limits} {
				if quantity, ok := list[name]; ok && quantity.Sign() <= 0 {
					return nil, fmt.Errorf("container %q resource %s %q must be greater than zero", container.Name, field, name)
				}
			}
			if previous, ok := seen[class.Name]; ok {
				if previous.ResourceName != name {
					return nil, fmt.Errorf("resources %q and %q both select DeviceClass %q; use one resource name per DeviceClass", previous.ResourceName, name, class.Name)
				}
			}
			profile, err := r.MatchDeviceClass(class)
			if err != nil {
				return nil, err
			}
			quantity := names[name]
			request := ResourceRequest{ResourceName: name, ContainerName: container.Name, DeviceClassName: class.Name, Profile: profile, Count: quantity.Value()}
			seen[class.Name] = request
			requests = append(requests, request)
		}
	}
	slices.SortFunc(requests, func(a, b ResourceRequest) int { return strings.Compare(a.DeviceClassName, b.DeviceClassName) })
	return requests, nil
}

// ValidateRequestContainers enforces the bridge's one-container-per-class allocation model.
func ValidateRequestContainers(requests []ResourceRequest) error {
	containers := make(map[string]string)
	for _, request := range requests {
		if previous, ok := containers[request.DeviceClassName]; ok {
			return fmt.Errorf("DRA DeviceClass %q is requested by multiple containers %q and %q; slurm-bridge currently supports one requesting container per DeviceClass", request.DeviceClassName, previous, request.ContainerName)
		}
		containers[request.DeviceClassName] = request.ContainerName
	}
	return nil
}
