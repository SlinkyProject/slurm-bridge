// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"context"
	"flag"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
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

	"github.com/SlinkyProject/slurm-bridge/internal/controller/pod/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/durationstore"
)

const (
	ControllerName = "pod-controller"

	// BackoffGCInterval is the time that has to pass before next iteration of backoff GC is run
	BackoffGCInterval = 1 * time.Minute
)

func init() {
	flag.IntVar(&maxConcurrentReconciles, "pod-workers", maxConcurrentReconciles, "Max concurrent workers for Pod controller.")
}

var (
	maxConcurrentReconciles = 1

	// this is a short cut for any sub-functions to notify the reconcile how long to wait to requeue
	durationStore = durationstore.NewDurationStore(durationstore.Greater)

	onceBackoffGC     sync.Once
	failedPodsBackoff = flowcontrol.NewBackOff(1*time.Second, 15*time.Minute)
)

// PodReconciler reconciles a Pod object
type PodReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	SchedulerName string

	SlurmClient slurmclient.Client
	EventCh     chan event.GenericEvent

	slurmControl  slurmcontrol.SlurmControlInterface
	eventRecorder record.EventRecorderLogger
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=delete;get;list;patch;update;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, retErr error) {
	logger := log.FromContext(ctx)

	logger.Info("Started syncing Pod", "request", req)

	onceBackoffGC.Do(func() {
		go wait.Until(failedPodsBackoff.GC, BackoffGCInterval, ctx.Done())
	})

	startTime := time.Now()
	defer func() {
		if retErr == nil {
			if res.RequeueAfter > 0 {
				logger.Info("Finished syncing Pod", "duration", time.Since(startTime), "result", res)
			} else {
				logger.Info("Finished syncing Pod", "duration", time.Since(startTime))
			}
		} else {
			logger.Info("Finished syncing Pod", "duration", time.Since(startTime), "error", retErr)
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
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	podEventHandler := &podEventHandler{
		SchedulerName: r.SchedulerName,
		Reader:        mgr.GetCache(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named(ControllerName).
		Watches(&corev1.Pod{}, podEventHandler).
		WatchesRawSource(source.Channel(r.EventCh, podEventHandler)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

func NewReconciler(kubeClient client.Client, slurmClient slurmclient.Client, schedulerName string, eventCh chan event.GenericEvent) *PodReconciler {
	scheme := kubeClient.Scheme()
	eventSource := corev1.EventSource{Component: ControllerName}
	eventRecorder := record.NewBroadcaster().NewRecorder(scheme, eventSource)
	r := &PodReconciler{
		Client:        kubeClient,
		Scheme:        scheme,
		EventCh:       eventCh,
		SchedulerName: schedulerName,
		SlurmClient:   slurmClient,
		slurmControl:  slurmcontrol.NewControl(slurmClient),
		eventRecorder: eventRecorder,
	}
	return r
}
