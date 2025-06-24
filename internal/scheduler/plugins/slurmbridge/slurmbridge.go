// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	sched "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"

	"github.com/SlinkyProject/slurm-bridge/internal/config"
	nodecontrollerutils "github.com/SlinkyProject/slurm-bridge/internal/controller/node/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"

	"github.com/puttsk/hostlist"
)

var (
	scheme = runtime.NewScheme()

	ErrorNoKubeNode        = errors.New("no more placeholder nodes to annotate pods")
	ErrorPodUpdateFailed   = errors.New("failed to update pod")
	ErrorNodeConfigInvalid = errors.New("requested node configuration is not available")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sched.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	utilruntime.Must(jobset.AddToScheme(scheme))
}

// Slurmbridge is a plugin that schedules pods in a group.
type SlurmBridge struct {
	client.Client
	schedulerName string
	slurmControl  slurmcontrol.SlurmControlInterface
	handle        framework.Handle
}

var _ framework.PreFilterPlugin = &SlurmBridge{}
var _ framework.FilterPlugin = &SlurmBridge{}

const (
	Name = "SlurmBridge"
)

// Name returns name of the plugin. It is used in logs, etc.
func (sb *SlurmBridge) Name() string {
	return Name
}

// New initializes and returns a new Slurmbridge plugin.
func New(ctx context.Context, obj runtime.Object, handle framework.Handle) (framework.Plugin, error) {

	logger := klog.FromContext(ctx)
	logger.V(5).Info("creating new SlurmBridge plugin")

	data, err := os.ReadFile(config.ConfigFile)
	if err != nil {
		logger.Error(err, "unable to read config file", "file", config.ConfigFile)
		// Attempt to read fallback debug config path
		data, err = os.ReadFile("/tmp/config.yaml.debug")
		if err != nil {
			logger.Error(err, "unable to read config file", "file", config.ConfigFile)
			return nil, err
		}
	}
	cfg := config.UnmarshalOrDie(data)

	client, err := client.New(handle.KubeConfig(), client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	clientConfig := &slurmclient.Config{
		Server: cfg.SlurmRestApi,
		AuthToken: func() string {
			token, _ := os.LookupEnv("SLURM_JWT")
			return token
		}(),
	}
	slurmClient, err := slurmclient.NewClient(clientConfig)
	if err != nil {
		logger.Error(err, "unable to create slurm client")
		return nil, err
	}
	sc := slurmcontrol.NewControl(slurmClient, cfg.MCSLabel, cfg.Partition)
	plugin := &SlurmBridge{
		Client:        client,
		schedulerName: cfg.SchedulerName,
		slurmControl:  sc,
		handle:        handle,
	}
	return plugin, nil
}

// PreFilter will check if a Slurm placeholder job has been created for the pod.
// If a placeholder job is not found, create one and return the pod to the scheduling
// queue.
// If a placeholder job is found, determine which node(s) have been assigned to the
// Slurm job and update state so the Filter plugin can filter out the assigned node(s)
func (sb *SlurmBridge) PreFilter(ctx context.Context, state *framework.CycleState, pod *corev1.Pod) (*framework.PreFilterResult, *framework.Status) {
	logger := klog.FromContext(ctx)

	// Populate podToJob representation to validate pod label and annotation
	if err := sb.validatePodToJob(ctx, pod); err != nil {
		logger.Error(err, "error validating pod against podToJob")
		return nil, framework.NewStatus(framework.Error, err.Error())
	}

	// If a placeholderJob exists and a node has been allocated, return immediately
	// as another pod has determined the placeholder job is running and assigned
	// a node to this pod.
	node := pod.Annotations[wellknown.AnnotationPlaceholderNode]
	if pod.Labels[wellknown.LabelPlaceholderJobId] != "" &&
		node != "" {
		phNode := make(sets.Set[string])
		phNode.Insert(node)
		return &framework.PreFilterResult{NodeNames: phNode}, framework.NewStatus(framework.Success)
	}

	// Construct an intermediate representation of the Slurm placeholder job
	slurmJobIR, err := slurmjobir.TranslateToSlurmJobIR(sb.Client, ctx, pod)
	if err != nil {
		return nil, framework.NewStatus(framework.Error, err.Error())
	}

	// Determine if a placeholder job for the pod exists in Slurm
	placeholderJob, err := sb.slurmControl.GetJob(ctx, pod)
	if err != nil {
		logger.Error(err, "error checking for Slurm job")
		return nil, framework.NewStatus(framework.Error, err.Error())
	}

	// Perform resource specific PreFilter
	fs := slurmjobir.PreFilter(sb.Client, ctx, pod, slurmJobIR)
	if fs.Code() != framework.Success {
		// If the placeholderjob is determined to no longer be valid
		// delete the placeholder job and remove the associated annotations
		for _, r := range fs.Reasons() {
			if r == slurmjobir.ErrorPlaceholderJobInvalid.Error() {
				logger.Error(err, "placeholder Job no longer valid, deleting job")
				err := sb.deletePlaceholderJob(ctx, pod)
				if err != nil {
					return nil, framework.NewStatus(framework.Error, err.Error())
				}
			}
		}
		return nil, fs
	}

	// Create a placeholder job in Slurm if needed
	if placeholderJob.JobId == 0 {
		jobid, err := sb.slurmControl.SubmitJob(ctx, pod, slurmJobIR)
		if err != nil {
			aggErrors := func() utilerrors.Aggregate {
				var target utilerrors.Aggregate
				_ = errors.As(err, &target)
				return target
			}().Errors()
			for _, e := range aggErrors {
				if strings.ToLower(e.Error()) == ErrorNodeConfigInvalid.Error() {
					logger.Error(err, "invalid node configuration for placeholder job")
					return nil, framework.NewStatus(framework.UnschedulableAndUnresolvable, e.Error())
				}
			}
			logger.Error(err, "error submitting Slurm job")
			return nil, framework.NewStatus(framework.Error, err.Error())
		}
		logger.V(5).Info("submitted placeholder to slurm", klog.KObj(pod))
		err = sb.labelPodsWithJobId(ctx, jobid, slurmJobIR)
		if err != nil {
			return nil, framework.NewStatus(framework.Error, err.Error())
		}
		return nil, framework.NewStatus(framework.Pending)
	} else {
		logger.V(4).Info("placeholder job exists")
		if placeholderJob.Nodes == "" {
			logger.V(4).Info("placeholder job exists but no nodes have been allocated")
			// As the placeholder job is not yet running, update to the job
			// to include any changes from slurmJobIR.
			jobid, err := sb.slurmControl.UpdateJob(ctx, pod, slurmJobIR)
			if err != nil {
				logger.Error(err, "error updating Slurm job")
				return nil, framework.NewStatus(framework.Pending, err.Error())
			}
			// Update the pods with the jobId label in case there
			// are new pods included in slurmJobIR after the update.
			err = sb.labelPodsWithJobId(ctx, jobid, slurmJobIR)
			if err != nil {
				logger.Error(err, "error labeling pods after update")
				return nil, framework.NewStatus(framework.Error, err.Error())
			}
			return nil, framework.NewStatus(framework.Pending, "no nodes assigned")
		}
		slurmNodes, _ := hostlist.Expand(placeholderJob.Nodes)
		kubeNodes, err := sb.slurmToKubeNodes(ctx, slurmNodes)
		if err != nil {
			return nil, framework.NewStatus(framework.Error, err.Error())
		}
		err = sb.annotatePodsWithNodes(ctx, placeholderJob.JobId, kubeNodes.Clone(), &slurmJobIR.Pods)
		if err != nil {
			return nil, framework.NewStatus(framework.Error, err.Error())
		}
		// Update pod after performing a Patch so subsequent plugins have
		// accurate annotations
		if err := sb.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return nil, framework.NewStatus(framework.Error, err.Error())
		}
		return &framework.PreFilterResult{NodeNames: kubeNodes}, framework.NewStatus(framework.Success, "")
	}
}

// annotatePodsWithNodes will annotate a jobid to pods and add a finalizer to
// ensure there is an opportunity to cleanly reconcile state between k8s and Slurm
func (sb *SlurmBridge) labelPodsWithJobId(ctx context.Context, jobid int32, slurmJobIR *slurmjobir.SlurmJobIR) error {
	logger := klog.FromContext(ctx)
	for _, p := range slurmJobIR.Pods.Items {
		if p.Labels == nil {
			p.Labels = make(map[string]string)
		}
		if p.Labels[wellknown.LabelPlaceholderJobId] == string(jobid) {
			continue
		}
		toUpdate := p.DeepCopy()
		toUpdate.Labels[wellknown.LabelPlaceholderJobId] = strconv.Itoa(int(jobid))
		toUpdate.Finalizers = append(toUpdate.Finalizers, wellknown.FinalizerScheduler)
		if err := sb.Patch(ctx, toUpdate, client.StrategicMergeFrom(&p)); err != nil {
			logger.Error(err, "failed to update pod with slurm job id")
			return ErrorPodUpdateFailed
		}
	}
	return nil
}

// annotatePodsWithNodes will annotate a node assignment to pods
func (sb *SlurmBridge) annotatePodsWithNodes(ctx context.Context, jobid int32, kubeNodes sets.Set[string], pods *corev1.PodList) error {
	logger := klog.FromContext(ctx)
	for _, p := range pods.Items {
		// Return if there are no nodes left
		if kubeNodes.Len() == 0 {
			logger.V(5).Info("no nodes left to annotate")
			break
		}
		// If this pod doesn't have a JobId that matches, it should be skipped as
		// it didn't exist when the placeholder job was created
		podJobID := slurmjobir.ParseSlurmJobId(p.Labels[wellknown.LabelPlaceholderJobId])
		if jobid != podJobID {
			logger.V(5).Info("pod JobID does not match placeholder JobID")
			continue
		}
		if p.Annotations == nil {
			p.Annotations = make(map[string]string)
		}
		node, ok := kubeNodes.PopAny()
		if !ok {
			logger.V(4).Info("could not get a node to assign")
			return ErrorNoKubeNode
		}
		toUpdate := p.DeepCopy()
		toUpdate.Annotations[wellknown.AnnotationPlaceholderNode] = node
		toleration := utils.NewTolerationNodeBridged(sb.schedulerName)
		toUpdate.Spec.Tolerations = utils.MergeTolerations(toUpdate.Spec.Tolerations, *toleration)
		if err := sb.Patch(ctx, toUpdate, client.StrategicMergeFrom(&p)); err != nil {
			logger.Error(err, "failed to update pod with slurm job id")
			return ErrorPodUpdateFailed
		}
	}
	return nil
}

// slurmToKubeNodes will translate slurm node names to kubernetes node names
func (sb *SlurmBridge) slurmToKubeNodes(ctx context.Context, slurmNodes []string) (sets.Set[string], error) {
	logger := klog.FromContext(ctx)

	nodeList := &corev1.NodeList{}
	if err := sb.List(ctx, nodeList); err != nil {
		logger.Error(err, "failed to list Kubernetes nodes")
		return nil, err
	}

	kubeNodes := make(sets.Set[string])
	nodeNameMap := nodecontrollerutils.MakeNodeNameMap(ctx, nodeList)
	for _, slurmNode := range slurmNodes {
		kubeNode, ok := nodeNameMap[slurmNode]
		if !ok {
			// Assume the kubeNode == slurmNode Name
			kubeNode = slurmNode
		}
		kubeNodes.Insert(kubeNode)
	}

	return kubeNodes, nil
}

// revertPlaceholderJob will delete the placeholder job associate with the pod
// and remove any annotations for pods in slurmJobIR that have a matching JobID.
func (sb *SlurmBridge) deletePlaceholderJob(ctx context.Context, pod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	// Construct an intermediate representation of the Slurm placeholder job
	slurmJobIR, err := slurmjobir.TranslateToSlurmJobIR(sb.Client, ctx, pod)
	if err != nil {
		logger.Error(err, "failed to translate to slurmjobir")
		return err
	}
	jobId := pod.Labels[wellknown.LabelPlaceholderJobId]
	if err := sb.slurmControl.DeleteJob(ctx, pod); err != nil {
		logger.Error(err, "failed to delete Slurm job for pod", "jobId", jobId, "pod", klog.KObj(pod))
		return err
	}
	for _, p := range slurmJobIR.Pods.Items {
		toUpdate := p.DeepCopy()
		if toUpdate.Labels[wellknown.LabelPlaceholderJobId] == "" {
			continue
		}
		if toUpdate.Labels[wellknown.LabelPlaceholderJobId] == jobId {
			delete(toUpdate.Labels, wellknown.LabelPlaceholderJobId)
			delete(toUpdate.Annotations, wellknown.AnnotationPlaceholderNode)
		}
		if err := sb.Patch(ctx, toUpdate, client.StrategicMergeFrom(&p)); err != nil {
			logger.Error(err, "failed to delete jobid and node annotation")
			return err
		}
	}
	return nil
}

// PreFilterExtensions returns a PreFilterExtensions interface if the plugin implements one.
func (sb *SlurmBridge) PreFilterExtensions() framework.PreFilterExtensions {
	return nil
}

// Filter will verify the node annotation matches the node being filtered.
func (sb *SlurmBridge) Filter(ctx context.Context, state *framework.CycleState, pod *corev1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("filter func", "pod", klog.KObj(pod), "node", nodeInfo.Node().Name)
	if pod.Annotations[wellknown.AnnotationPlaceholderNode] == nodeInfo.GetName() {
		return framework.NewStatus(framework.Success, "")
	}
	return framework.NewStatus(framework.Unschedulable, "node does not match annotation")
}

func (sb *SlurmBridge) validatePodToJob(ctx context.Context, pod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("validatePodToJob func", "pod", klog.KObj(pod))
	namespacedName := types.NamespacedName{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	}
	podToJob, err := sb.slurmControl.GetJobsForPods(ctx)
	if err != nil {
		logger.Error(err, "error populating podToJob")
		return err
	}
	if val, ok := (*podToJob)[namespacedName.String()]; ok {
		toUpdate := pod.DeepCopy()
		// If the pod has a JobId set, validate it against podToJob
		if pod.Labels[wellknown.LabelPlaceholderJobId] != "" &&
			val.JobId != slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelPlaceholderJobId]) {
			logger.V(3).Info("Pod jobId label does not match Slurm", "pod", klog.KObj(pod),
				"jobId label", pod.Labels[wellknown.LabelPlaceholderJobId],
				"slurm job", val)
			toUpdate.Labels[wellknown.LabelPlaceholderJobId] = strconv.Itoa(int(val.JobId))
		}
		// If the pod has a Node set, validate it against podToJob
		nodes, _ := hostlist.Expand(val.Nodes)
		if pod.Annotations[wellknown.AnnotationPlaceholderNode] != "" &&
			!slices.Contains(nodes, pod.Annotations[wellknown.AnnotationPlaceholderNode]) {
			logger.V(3).Info("Pod node annotation does not match Slurm nodes", "pod", klog.KObj(pod),
				"node annotation", pod.Annotations[wellknown.AnnotationPlaceholderNode],
				"slurm job", val)
			toUpdate.Annotations[wellknown.AnnotationPlaceholderNode] = ""
		}
		if !reflect.DeepEqual(pod, toUpdate) {
			if err := sb.Patch(ctx, toUpdate, client.StrategicMergeFrom(pod)); err != nil {
				logger.Error(err, "failed to update pod with slurm job id")
				return ErrorPodUpdateFailed
			}
			// Update pod to reflect patch
			pod.Labels[wellknown.LabelPlaceholderJobId] = toUpdate.Labels[wellknown.LabelPlaceholderJobId]
			pod.Annotations[wellknown.AnnotationPlaceholderNode] = toUpdate.Annotations[wellknown.AnnotationPlaceholderNode]
		}
	}
	return nil
}
