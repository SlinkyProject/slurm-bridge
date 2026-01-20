// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package pod

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/pod/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/durationstore"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
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
	if r.EventCh != nil {
		r.setupEventHandler()
	}
	return r
}

func (r *PodReconciler) setupEventHandler() {
	logger := log.FromContext(context.Background())
	informer := r.SlurmClient.GetInformer(slurmtypes.ObjectTypeV0044JobInfo)
	if informer == nil {
		return
	}
	informer.SetEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			job, ok := obj.(*slurmtypes.V0044JobInfo)
			if !ok {
				logger.Error(fmt.Errorf("expected V0044JobInfo"), "failed to cast object")
				return
			}
			// Ignore event if a placeholderInfo struct can not be parsed
			phInfo := &placeholderinfo.PlaceholderInfo{}
			if err := placeholderinfo.ParseIntoPlaceholderInfo(job.AdminComment, phInfo); err != nil {
				return
			}
			jobId := ptr.Deref(job.JobId, 0)
			r.generatePodEvents(jobId, true)
		},
		UpdateFunc: func(oldObj, newObj any) {
			jobOld, ok := oldObj.(*slurmtypes.V0044JobInfo)
			if !ok {
				logger.Error(fmt.Errorf("expected V0044JobInfo"), "failed to cast old object")
				return
			}
			jobNew, ok := newObj.(*slurmtypes.V0044JobInfo)
			if !ok {
				logger.Error(fmt.Errorf("expected V0044JobInfo"), "failed to cast new object")
				return
			}
			// Ignore event if a placeholderInfo struct can not be parsed
			phInfo := &placeholderinfo.PlaceholderInfo{}
			if err := placeholderinfo.ParseIntoPlaceholderInfo(jobNew.AdminComment, phInfo); err != nil {
				return
			}
			jobId := ptr.Deref(jobNew.JobId, 0)
			if !apiequality.Semantic.DeepEqual(jobNew.JobState, jobOld.JobState) {
				r.generatePodEvents(jobId, false)
			}
		},
		DeleteFunc: func(obj any) {
			job, ok := obj.(*slurmtypes.V0044JobInfo)
			if !ok {
				logger.Error(fmt.Errorf("expected V0044JobInfo"), "failed to cast object")
				return
			}
			// Ignore event if a placeholderInfo struct can not be parsed
			phInfo := &placeholderinfo.PlaceholderInfo{}
			if err := placeholderinfo.ParseIntoPlaceholderInfo(job.AdminComment, phInfo); err != nil {
				return
			}
			jobId := ptr.Deref(job.JobId, 0)
			r.generatePodEvents(jobId, true)
		},
	})
}

// generatePodEvents will enqueue generic pod events for each pod labeled with a jobId
func (r *PodReconciler) generatePodEvents(jobId int32, delete bool) {
	ctx := context.Background()
	logger := log.FromContext(ctx)

	pods := &corev1.PodList{}
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{wellknown.LabelPlaceholderJobId: strconv.Itoa(int(jobId))}),
	}
	if err := r.List(ctx, pods, opts); err != nil {
		logger.Error(err, "failed to get pods")
		return
	}

	logger.V(1).Info("Generating pod reconcile requests", "jobId", jobId, "requests", len(pods.Items))
	for _, p := range pods.Items {
		r.EventCh <- event.GenericEvent{Object: &p}
	}

	if delete && len(pods.Items) == 0 {
		logger.Info("Terminating Slurm Job, its Pods were deleted", "jobId", jobId)
		if err := r.slurmControl.TerminateJob(ctx, jobId); err != nil {
			logger.Error(err, "failed to terminate Slurm Job without corresponding Pod",
				"jobId", jobId)
		}
	}
}
