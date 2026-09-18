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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nodeutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
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

	name, ok, err := nodeutils.GetNodeNameForSlurmName(ctx, h.Reader, node.GetName())
	if err != nil {
		logger.Error(err, "failed to resolve node")
		return
	}
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

	poolSlices, err := nodeutils.GetResourceSlicesForPool(ctx, r.Client, resourceSlice.Spec.Driver, resourceSlice.Spec.Pool.Name)
	if err != nil {
		logger.Error(err, "failed to list ResourceSlices for pool", "resourceSlice", client.ObjectKeyFromObject(resourceSlice))
		return nil
	}
	// Include the event object: an old or deleted slice may no longer be cached.
	poolSlices = append(poolSlices, *resourceSlice)
	nodeNames := sets.New[string]()
	for i := range poolSlices {
		poolSlice := &poolSlices[i]
		if dra.ValidateResourceSliceNode(poolSlice) == nil {
			nodeNames.Insert(*poolSlice.Spec.NodeName)
			continue
		}
		// Unsupported selection has no single node owner. Reconcile all nodes
		// so their inventory validation reports the unsupported pool.
		nodes := &corev1.NodeList{}
		if err := r.List(ctx, nodes); err != nil {
			logger.Error(err, "failed to list nodes for ResourceSlice", "resourceSlice", client.ObjectKeyFromObject(poolSlice))
			return nil
		}
		for _, node := range nodes.Items {
			nodeNames.Insert(node.Name)
		}
		break
	}

	requests := make([]reconcile.Request, 0, nodeNames.Len())
	for _, name := range sets.List(nodeNames) {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
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
