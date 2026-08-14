// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type nodeEventHandler struct {
	client.Reader
}

// Create implements handler.EventHandler.
func (h *nodeEventHandler) Create(ctx context.Context, evt event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// Intentionally blank
}

// Delete implements handler.EventHandler.
func (h *nodeEventHandler) Delete(ctx context.Context, evt event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// Intentionally blank
}

// Generic implements handler.EventHandler.
func (h *nodeEventHandler) Generic(ctx context.Context, evt event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	logger := log.FromContext(ctx)

	node, ok := evt.Object.(*corev1.Node)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("event object is not a node %#v", evt.Object))
		return
	}

	nodeList := &corev1.NodeList{}
	if err := h.List(ctx, nodeList); err != nil {
		logger.Error(err, "failed to list nodes")
		return
	}
	nodeNameMap := nodeutils.MakeNodeNameMap(ctx, nodeList)

	name, ok := nodeNameMap[node.GetName()]
	if !ok {
		name = node.GetName()
	}
	namespacedName := types.NamespacedName{
		Name: name,
	}
	if err := h.Get(ctx, namespacedName, node); err != nil {
		logger.Error(err, "failed to get node")
		return
	}
	enqueueNode(q, node)
}

// Update implements handler.EventHandler.
func (h *nodeEventHandler) Update(ctx context.Context, evt event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// Intentionally blank
}

var _ handler.EventHandler = &nodeEventHandler{}

func (r *NodeReconciler) resourceSliceToNodes(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx)
	resourceSlice, ok := obj.(*resourcev1.ResourceSlice)
	if !ok || !r.draRegistry.SupportsDriver(resourceSlice.Spec.Driver) {
		return nil
	}

	// This should be the most common case - a ResourceSlice has NodeName set.
	if resourceSlice.Spec.NodeName != nil && !ptr.Deref(resourceSlice.Spec.PerDeviceNodeSelection, false) {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: *resourceSlice.Spec.NodeName}}}
	}

	// This handles cases where a ResourceSlice doesn't correspond to exactly one node.
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		logger.Error(err, "failed to list nodes for ResourceSlice", "resourceSlice", client.ObjectKeyFromObject(resourceSlice))
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if _, external := node.Labels[wellknown.LabelExternalNode]; !external {
			continue
		}
		matches, err := dra.ResourceSliceMatchesNode(node, resourceSlice)
		if err != nil {
			logger.Error(err, "failed to match ResourceSlice to node", "resourceSlice", client.ObjectKeyFromObject(resourceSlice), "node", client.ObjectKeyFromObject(node))
			continue
		}
		if matches {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		}
	}
	return requests
}

func enqueueNode(q workqueue.TypedRateLimitingInterface[reconcile.Request], node *corev1.Node) {
	enqueueNodeAfter(q, node, 0)
}

func enqueueNodeAfter(q workqueue.TypedRateLimitingInterface[reconcile.Request], node *corev1.Node, duration time.Duration) {
	if node == nil {
		return
	}
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: node.GetName(),
		},
	}
	q.AddAfter(req, duration)
}
