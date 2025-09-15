// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"flag"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/node/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/durationstore"
)

const (
	// BackoffGCInterval is the time that has to pass before next iteration of backoff GC is run
	BackoffGCInterval = 1 * time.Minute
)

func init() {
	flag.IntVar(&maxConcurrentReconciles, "node-workers", maxConcurrentReconciles, "Max concurrent workers for Node controller.")
}

var (
	maxConcurrentReconciles = 1

	// this is a short cut for any sub-functions to notify the reconcile how long to wait to requeue
	durationStore = durationstore.NewDurationStore(durationstore.Greater)

	onceBackoffGC     sync.Once
	failedPodsBackoff = flowcontrol.NewBackOff(1*time.Second, 15*time.Minute)
)

// NodeReconciler reconciles a Node object
type NodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	SchedulerName string
	SlurmClient   slurmclient.Client
	EventCh       chan event.GenericEvent

	slurmControl  slurmcontrol.SlurmControlInterface
	eventRecorder record.EventRecorderLogger
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, retErr error) {
	logger := log.FromContext(ctx)

	logger.Info("Started syncing Node", "request", req)

	onceBackoffGC.Do(func() {
		go wait.Until(failedPodsBackoff.GC, BackoffGCInterval, ctx.Done())
	})

	startTime := time.Now()
	defer func() {
		if retErr == nil {
			if res.RequeueAfter > 0 {
				logger.Info("Finished syncing Node", "duration", time.Since(startTime), "result", res)
			} else {
				logger.Info("Finished syncing Node", "duration", time.Since(startTime))
			}
		} else {
			logger.Info("Finished syncing Node", "duration", time.Since(startTime), "error", retErr)
		}
		// clean the duration store
		_ = durationStore.Pop(req.String())
	}()

	retErr = r.Sync(ctx, req)
	res = reconcile.Result{
		RequeueAfter: durationStore.Pop(req.String()),
	}
	return res, retErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.setupInternal()
	nodeEventHandler := &nodeEventHandler{
		Reader: mgr.GetCache(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("node-controller").
		For(&corev1.Node{}).
		WatchesRawSource(source.Channel(r.EventCh, nodeEventHandler)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

func (r *NodeReconciler) setupInternal() {
	if r.eventRecorder == nil {
		r.eventRecorder = record.NewBroadcaster().NewRecorder(r.Scheme, corev1.EventSource{Component: "node-controller"})
	}
	if r.slurmControl == nil {
		r.slurmControl = slurmcontrol.NewControl(r.SlurmClient)
	}
	if r.EventCh != nil {
		r.setupEventHandler()
	}
}

func (r *NodeReconciler) setupEventHandler() {
	logger := log.FromContext(context.Background())
	informer := r.SlurmClient.GetInformer(slurmtypes.ObjectTypeV0043Node)
	if informer == nil {
		return
	}
	informer.SetEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, ok := obj.(*slurmtypes.V0043Node)
			if !ok {
				logger.Error(fmt.Errorf("expected V0043Node"), "failed to cast object")
				return
			}
			r.EventCh <- nodeEvent(*node.Name)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			nodeOld, ok := oldObj.(*slurmtypes.V0043Node)
			if !ok {
				logger.Error(fmt.Errorf("expected V0043Node"), "failed to cast old object")
				return
			}
			nodeNew, ok := newObj.(*slurmtypes.V0043Node)
			if !ok {
				logger.Error(fmt.Errorf("expected V0043Node"), "failed to cast new object")
				return
			}
			if !apiequality.Semantic.DeepEqual(nodeNew.Address, nodeOld.Address) ||
				!apiequality.Semantic.DeepEqual(nodeNew.Hostname, nodeOld.Hostname) {
				r.EventCh <- nodeEvent(*nodeNew.Name)
			}
		},
		DeleteFunc: func(obj interface{}) {
			node, ok := obj.(*slurmtypes.V0043Node)
			if !ok {
				logger.Error(fmt.Errorf("expected V0043Node"), "failed to cast object")
				return
			}
			r.EventCh <- nodeEvent(*node.Name)
		},
	})
}

func nodeEvent(name string) event.GenericEvent {
	return event.GenericEvent{
		Object: &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func New(client client.Client, scheme *runtime.Scheme, schedulerName string, eventCh chan event.GenericEvent, slurmClient slurmclient.Client) *NodeReconciler {
	r := &NodeReconciler{
		Client:        client,
		SchedulerName: schedulerName,
		Scheme:        scheme,
		EventCh:       eventCh,
		SlurmClient:   slurmClient,
	}
	r.setupInternal()
	return r
}
