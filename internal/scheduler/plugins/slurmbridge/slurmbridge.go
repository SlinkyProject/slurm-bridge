// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	lws "sigs.k8s.io/lws/api/leaderworkerset/v1"
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
	ErrorNoKubeNode           = errors.New("no more placeholder nodes to annotate pods")
	ErrorNoKubeNodeMatch      = errors.New("slurm node matches no Kube nodes")
	ErrorPodUpdateFailed      = errors.New("failed to update pod")
	ErrorNodeConfigInvalid    = errors.New("requested node configuration is not available")
	ErrorNoNodesAssigned      = errors.New("no nodes assigned to job")
	ErrorPodWithResourceClaim = errors.New("can't schedule pod with a resource claim")
)

func init() {
	utilruntime.Must(scheme.AddToScheme(scheme.Scheme))
	utilruntime.Must(sched.AddToScheme(scheme.Scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme.Scheme))
	utilruntime.Must(jobset.AddToScheme(scheme.Scheme))
	utilruntime.Must(lws.AddToScheme(scheme.Scheme))
}

// Slurmbridge is a plugin that schedules pods in a group.
type SlurmBridge struct {
	client.Client
	schedulerName string
	slurmControl  slurmcontrol.SlurmControlInterface
	handle        fwk.Handle
}

var _ fwk.PreEnqueuePlugin = &SlurmBridge{}
var _ fwk.PreFilterPlugin = &SlurmBridge{}
var _ fwk.FilterPlugin = &SlurmBridge{}
var _ fwk.PostFilterPlugin = &SlurmBridge{}
var _ fwk.PreBindPlugin = &SlurmBridge{}

const (
	Name                  = "SlurmBridge"
	stateKey fwk.StateKey = Name
)

// Name returns name of the plugin. It is used in logs, etc.
func (sb *SlurmBridge) Name() string {
	return Name
}

type stateData struct {
	slurmJobIR *slurmjobir.SlurmJobIR
}

func (d *stateData) Clone() fwk.StateData {
	return d
}

func getStateData(cs fwk.CycleState) (*stateData, error) {
	state, err := cs.Read(stateKey)
	if err != nil {
		return nil, err
	}
	s, ok := state.(*stateData)
	if !ok {
		return nil, errors.New("unable to convert state into stateData")
	}
	return s, nil
}

// New initializes and returns a new Slurmbridge plugin.
func New(ctx context.Context, obj runtime.Object, handle fwk.Handle) (fwk.Plugin, error) {

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

	client, err := client.New(handle.KubeConfig(), client.Options{})
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

// PreEnqueue will add the slurm-bridge toleration to the pod.
func (sb *SlurmBridge) PreEnqueue(ctx context.Context, pod *corev1.Pod) *fwk.Status {

	logger := klog.FromContext(ctx)
	logger.V(5).Info("adding toleration to pod", "pod", klog.KObj(pod))

	toUpdate := pod.DeepCopy()
	toleration := utils.NewTolerationNodeBridged(sb.schedulerName)
	toUpdate.Spec.Tolerations = utils.MergeTolerations(toUpdate.Spec.Tolerations, *toleration)
	if err := sb.Patch(ctx, toUpdate, client.StrategicMergeFrom(pod)); err != nil {
		logger.Error(err, "failed to update pod with slurm job id")
		return fwk.NewStatus(fwk.Unschedulable, "error patching finalizer")
	}
	// Update pod data after performing a Patch
	if err := sb.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		return fwk.NewStatus(fwk.Error, err.Error())
	}
	return fwk.NewStatus(fwk.Success)
}

// PreFilter will check if a Slurm placeholder job has been created for the pod.
// If a placeholder job is not found, create one and return the pod to the scheduling
// queue.
// If a placeholder job is found, determine which node(s) have been assigned to the
// Slurm job and update state so the Filter plugin can filter out the assigned node(s)
func (sb *SlurmBridge) PreFilter(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, nodeInfo []fwk.NodeInfo) (*fwk.PreFilterResult, *fwk.Status) {
	logger := klog.FromContext(ctx)
	var err error

	if pod.Spec.ResourceClaims != nil {
		logger.Error(ErrorPodWithResourceClaim, "use extended resource or device plugin request instead")
		return nil, fwk.NewStatus(fwk.Unschedulable, ErrorPodWithResourceClaim.Error())
	}

	s := &stateData{}
	state.Write(stateKey, s)

	// Populate podToJob representation to validate pod label and annotation
	if err := sb.validatePodToJob(ctx, pod); err != nil {
		logger.Error(err, "error validating pod against podToJob")
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}

	// Construct an intermediate representation of the Slurm placeholder job
	s.slurmJobIR, err = slurmjobir.TranslateToSlurmJobIR(sb.Client, ctx, pod)
	if err != nil {
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}

	// If a placeholderJob exists and a node has been allocated, return immediately
	// as another pod has determined the placeholder job is running and assigned
	// a node to this pod.
	node := pod.Annotations[wellknown.AnnotationPlaceholderNode]
	if pod.Labels[wellknown.LabelPlaceholderJobId] != "" &&
		node != "" {
		phNode := make(sets.Set[string])
		phNode.Insert(node)
		return &fwk.PreFilterResult{NodeNames: phNode}, fwk.NewStatus(fwk.Success)
	}

	// Determine if a placeholder job for the pod exists in Slurm
	placeholderJob, err := sb.slurmControl.GetJob(ctx, pod)
	if err != nil {
		logger.Error(err, "error checking for Slurm job")
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}

	// Perform resource specific PreFilter
	fs := slurmjobir.PreFilter(sb.Client, ctx, pod, s.slurmJobIR)
	if fs.Code() != fwk.Success {
		// If the placeholderjob is determined to no longer be valid
		// delete the placeholder job and remove the associated annotations
		for _, r := range fs.Reasons() {
			if r == slurmjobir.ErrorPlaceholderJobInvalid.Error() {
				logger.Error(err, "placeholder Job no longer valid, deleting job")
				err := sb.deletePlaceholderJob(ctx, pod)
				if err != nil {
					return nil, fwk.NewStatus(fwk.Error, err.Error())
				}
			}
		}
		return nil, fs
	}

	// If a placeholder job does not exist, this plugin should return as a success
	// with no PreFilterResult. Because Filter will ultimately detect that slurm
	// has not scheduled any nodes to the pods (no node annotation), the PostFilter
	// plugin will be invoked. The placeholder job will then be created in the
	// PostFilter plugin stage. If a placeholder job exists and is running, update
	// pods with node assignments.
	if placeholderJob.JobId == 0 {
		return nil, fwk.NewStatus(fwk.Success)
	} else {
		logger.V(4).Info("placeholder job exists")
		if placeholderJob.Nodes == "" {
			logger.V(4).Info("placeholder job exists but no nodes have been allocated")
			return nil, fwk.NewStatus(fwk.Pending, ErrorNoNodesAssigned.Error())
		}
		// The placeholder job is running. Assign nodes to pods.
		slurmNodes, _ := hostlist.Expand(placeholderJob.Nodes)
		kubeNodes, err := sb.slurmToKubeNodes(ctx, slurmNodes)
		if err != nil {
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		err = sb.annotatePodsWithNodes(ctx, placeholderJob.JobId, kubeNodes.Clone(), &s.slurmJobIR.Pods)
		if err != nil {
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		// Update pod after performing a Patch so subsequent plugins have
		// accurate annotations
		if err := sb.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		// By passing the list of nodes in the placeholder job as PreFilterResult,
		// Filter plugins will only run for nodes in the Slurm job. This is the final
		// PreFilter step that must occur before pods are allowed to run.
		return &fwk.PreFilterResult{NodeNames: kubeNodes}, fwk.NewStatus(fwk.Success, "")
	}
}

// PostFilter will create the Slurm placeholder job once the pod has been
// processed by the PreFilter and Filter plugins. This allows the rest of
// the kubernetes plugins to have a say in which pods would be feasible for
// Slurm to schedule the pod(s) on.
func (sb *SlurmBridge) PostFilter(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, m fwk.NodeToStatusReader) (*fwk.PostFilterResult, *fwk.Status) {
	logger := klog.FromContext(ctx)

	s, err := getStateData(state)
	if err != nil {
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}

	// Determine if a placeholder job for the pod exists in Slurm
	placeholderJob, err := sb.slurmControl.GetJob(ctx, pod)
	if err != nil {
		logger.Error(err, "error checking for Slurm job")
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}

	// Create the Slurm placeholder job based on the nodes that have
	// not been filtered out by Filter plugins. Because the SlurmBridge
	// Filter plugin runs last, and will fail if the node annotation does
	// not match, a failure from SlurmBridge means none of the other
	// Filter plugins rejected the node and it can be fed into Slurm
	// as a node to schedule with.
	feasibleNodes, err := m.NodesForStatusCode(sb.handle.SnapshotSharedLister().NodeInfos(), fwk.Unschedulable)
	if err != nil {
		logger.Error(err, "error getting nodes that SlurmBridge can use")
		return nil, fwk.NewStatus(fwk.Error, err.Error())
	}
	for _, node := range feasibleNodes {
		status := m.Get(node.Node().Name)
		// If the Unschedulable code was set by SlurmBridge
		// that means no other plugin filtered out this node.
		// As long as the node is known to Slurm, we will include
		// this node for consideration.
		if status.Plugin() == Name {
			slurmName := nodecontrollerutils.GetSlurmNodeName(node.Node())
			if isSlurm, _ := sb.slurmControl.IsSlurmNode(ctx, slurmName); isSlurm {
				s.slurmJobIR.JobInfo.Nodes = append(s.slurmJobIR.JobInfo.Nodes, slurmName)
			}
		}
	}

	// If this situation occurs, the best we can do is trigger another
	// scheduling cycle.
	if len(s.slurmJobIR.JobInfo.Nodes) < len(s.slurmJobIR.Pods.Items) {
		return nil, fwk.NewStatus(fwk.Success)
	}

	// If no placeholder job exists, we should create one with the list
	// of nodes that passed Filter plugins.
	if placeholderJob.JobId == 0 {
		jobid, err := sb.slurmControl.SubmitJob(ctx, pod, s.slurmJobIR)
		if err != nil {
			aggErrors := func() utilerrors.Aggregate {
				var target utilerrors.Aggregate
				_ = errors.As(err, &target)
				return target
			}().Errors()
			for _, e := range aggErrors {
				if strings.ToLower(e.Error()) == ErrorNodeConfigInvalid.Error() {
					logger.Error(err, "invalid node configuration for placeholder job")
					return nil, fwk.NewStatus(fwk.UnschedulableAndUnresolvable, e.Error())
				}
			}
			logger.Error(err, "error submitting Slurm job")
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		logger.V(5).Info("submitted placeholder to slurm", "pod", klog.KObj(pod))
		err = sb.labelPodsWithJobId(ctx, jobid, s.slurmJobIR)
		if err != nil {
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		return nil, fwk.NewStatus(fwk.Success)
	}

	logger.V(4).Info("placeholder job exists")
	if placeholderJob.Nodes == "" {
		logger.V(4).Info("placeholder job exists but no nodes have been allocated")
		// As the placeholder job is not yet running, update to the job
		// to include any changes from slurmJobIR.
		jobid, err := sb.slurmControl.UpdateJob(ctx, pod, s.slurmJobIR)
		if err != nil {
			logger.Error(err, "error updating Slurm job")
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		// Update the pods with the jobId label in case there
		// are new pods included in slurmJobIR after the update.
		err = sb.labelPodsWithJobId(ctx, jobid, s.slurmJobIR)
		if err != nil {
			logger.Error(err, "error labeling pods after update")
			return nil, fwk.NewStatus(fwk.Error, err.Error())
		}
		return nil, fwk.NewStatus(fwk.Success, ErrorNoNodesAssigned.Error())
	}

	// If we get here, that means the job started running after PreFilter occurred.
	// Return a success so the pod will get another PreFilter attempt.
	return nil, fwk.NewStatus(fwk.Success, "")
}

// PreBindPreFlight will check if any GRES was requested for the placeholder job
func (sb *SlurmBridge) PreBindPreFlight(ctx context.Context, cs fwk.CycleState, pod *corev1.Pod, nodeName string) *fwk.Status {
	return nil
}

// PreBind will generate ResourceClaims for any GRES allocation in Slurm.
// If a GRES allocation does not have a corresponding DeviceClass, it will
// be skipped.
func (sb *SlurmBridge) PreBind(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, nodeName string) *fwk.Status {

	// Note that whole node allocations in slurm will look like all
	// resources were requested, but that doesn't mean the pod
	// intended to use them.
	resources, err := sb.slurmControl.GetResources(ctx, pod, nodeName)
	if err != nil {
		return fwk.NewStatus(fwk.Error, err.Error())
	}

	err = sb.preBindExtendedResources(ctx, pod, nodeName, resources)
	if err != nil {
		return fwk.NewStatus(fwk.Error, err.Error())
	}

	return nil
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
			// If the slurmNode exists as a kube node, they are
			// assumed to be the same node. If not, return an error
			// that the slurm job included an unknown node.
			if sb.handle.ClientSet() != nil {
				if _, err := sb.handle.ClientSet().CoreV1().Nodes().Get(ctx, slurmNode, metav1.GetOptions{}); apierrors.IsNotFound(err) {
					out := fmt.Sprintf("no matching kube nodes for Slurm node: %s", slurmNode)
					logger.Error(ErrorNoKubeNodeMatch, out)
					return nil, ErrorNoKubeNodeMatch
				}
				kubeNode = slurmNode
			} else {
				return nil, ErrorNoKubeNodeMatch
			}

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
func (sb *SlurmBridge) PreFilterExtensions() fwk.PreFilterExtensions {
	return nil
}

// Filter will verify the node annotation matches the node being filtered.
// This must be the last configured Filter plugin so PostFilter can make
// the assertion that a failure from this Filter plugin implies no other
// Filter plugin removed the node from consideration before getting here.
func (sb *SlurmBridge) Filter(ctx context.Context, state fwk.CycleState, pod *corev1.Pod, nodeInfo fwk.NodeInfo) *fwk.Status {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("filter func", "pod", klog.KObj(pod), "node", nodeInfo.Node().Name)
	if pod.Annotations[wellknown.AnnotationPlaceholderNode] == nodeInfo.Node().Name {
		return fwk.NewStatus(fwk.Success, "")
	}
	return fwk.NewStatus(fwk.Unschedulable, "node does not match annotation")
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
