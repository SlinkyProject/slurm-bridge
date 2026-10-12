// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/lru"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmutils "github.com/SlinkyProject/slurm-client/pkg/utils"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmconstraint"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjob"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type ExternalJob = slurmjob.ExternalJob

func isActiveJob(job *api.V0044JobInfo) bool {
	state := slurmutils.GetStateAsSet(job.JobState)
	return state.Len() == 0 || !state.HasAny(
		api.V0044JobInfoJobStateCANCELLED,
		api.V0044JobInfoJobStateCOMPLETED,
	)
}

type SlurmControlInterface interface {
	GetResources(ctx context.Context, pod *corev1.Pod, nodeName string) (*NodeResources, error)
	DeleteJob(ctx context.Context, pod *corev1.Pod) error
	GetJobsForPods(ctx context.Context, pods ...*corev1.Pod) (*map[string]ExternalJob, error)
	VerifyJobsForPods(ctx context.Context, candidates map[string]ExternalJob, pods ...*corev1.Pod) (*map[string]ExternalJob, error)
	GetJob(ctx context.Context, pod *corev1.Pod) (*ExternalJob, error)
	GetNodeNames(ctx context.Context, partition *string) ([]string, error)
	SubmitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) ([]int32, error)
	UpdateJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error)
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
	*slurmjob.PodIndex
	mcsLabel  string
	partition string
	// lastUpdate holds the last job description sent per pending job ID, so an
	// unchanged UpdateJob skips its REST round trip. Size-capped so jobs that are
	// never looked up again can't grow it without bound.
	lastUpdate *lru.Cache
}

type NodeResources struct {
	Node           string
	NodeExtra      string
	SocketsPerNode int32
	CoresPerSocket int32
	MemAlloc       int64
	CoreBitmap     string
	Channel        int32
	Gres           []GresLayout
}

type GresLayout struct {
	Count int64
	Index string
	Name  string
	Type  string
}

func sharedFromExclusiveAnnotation(slurmJobComponent *slurmjobir.SlurmJobComponent) *[]api.V0044JobDescMsgShared {
	exclusive := true
	if slurmJobComponent != nil && slurmJobComponent.JobInfo.Exclusive != nil {
		exclusive = *slurmJobComponent.JobInfo.Exclusive
	}
	if exclusive {
		return &[]api.V0044JobDescMsgShared{api.V0044JobDescMsgSharedNone}
	}
	return &[]api.V0044JobDescMsgShared{api.V0044JobDescMsgSharedMcs}
}

// gresCompatibilityConstraint requires the GRES compatibility feature on every
// bridge job while preserving the meaning of any user-supplied constraints.
func gresCompatibilityConstraint(constraints *string) (*string, error) {
	composed, err := slurmconstraint.Compose(wellknown.SlurmFeatureGRESCompatible, ptr.Deref(constraints, ""))
	if err != nil {
		return nil, err
	}
	return ptr.To(composed), nil
}

// DeleteSlurmJob will delete an external job
func (r *realSlurmControl) DeleteJob(ctx context.Context, pod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	job := &api.V0044JobInfo{}
	jobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
	if jobId == 0 {
		return nil
	}
	job.JobId = &jobId
	r.lastUpdate.Remove(jobId)
	if err := r.Delete(ctx, job); err != nil {
		logger.Error(err, "failed to delete Slurm job", "jobId", jobId)
		return err
	}
	return nil
}

// JobForPod matches a pod's UID, falling back to namespace/name for legacy jobs.
func JobForPod(jobs map[string]ExternalJob, pod *corev1.Pod) (ExternalJob, bool) {
	return slurmjob.JobForPod(jobs, pod)
}

// GetJob will check if an external job has been created for a given pod
func (r *realSlurmControl) GetJob(ctx context.Context, pod *corev1.Pod) (*ExternalJob, error) {
	logger := klog.FromContext(ctx)
	jobOut := ExternalJob{}

	job := &api.V0044JobInfo{}
	jobIDLabel := pod.Labels[wellknown.LabelExternalJobId]
	if jobIDLabel == "" {
		return &jobOut, nil
	}
	// Only pending jobs are updated; forget the rest so entries do not outlive their job.
	defer func() {
		if !jobOut.Pending {
			r.lastUpdate.Remove(slurmjobir.ParseSlurmJobId(jobIDLabel))
		}
	}()

	err := r.Get(ctx, object.ObjectKey(jobIDLabel), job, &client.GetOptions{SkipCache: true})
	if err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return &jobOut, nil
		}
		logger.Error(err, "could not get job for pod", "pod", klog.KObj(pod))
		return nil, err
	}

	expectedJobID := slurmjobir.ParseSlurmJobId(jobIDLabel)
	if actualJobID := ptr.Deref(job.JobId, 0); actualJobID != expectedJobID {
		return nil, fmt.Errorf("slurm returned job %d when requesting job %d", actualJobID, expectedJobID)
	}

	if !isActiveJob(job) {
		return &jobOut, nil
	}
	logger.V(5).Info("found matching job")
	jobOut, err = slurmjob.FromJobInfo(job)
	if err != nil {
		return nil, err
	}

	return &jobOut, nil
}

// SubmitJob submits an external job to Slurm for a node placement decision. The
// external job is later used to determine which node to bind a k8s pod to.
func (r *realSlurmControl) SubmitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) ([]int32, error) {
	if err := slurmJobIR.Validate(); err != nil {
		return []int32{}, err
	}

	return r.submitJob(ctx, pod, slurmJobIR)
}

// UpdateJob updates an external job
func (r *realSlurmControl) UpdateJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error) {
	logger := klog.FromContext(ctx)

	if err := slurmJobIR.Validate(); err != nil {
		return int32(0), err
	}

	componentIndex := slurmJobIR.ComponentOf(pod.Namespace, pod.Name)
	if componentIndex == -1 {
		return int32(0), fmt.Errorf("invalid component index %v for pod %v", componentIndex, klog.KObj(pod))
	}
	component := slurmJobIR.Components[componentIndex]

	jobIDValue, err := strconv.ParseInt(
		pod.Labels[wellknown.LabelExternalJobId],
		10,
		32,
	)
	if err != nil {
		return 0, fmt.Errorf(
			"unable to parse job ID label %q for pod %v: %w",
			pod.Labels[wellknown.LabelExternalJobId],
			klog.KObj(pod),
			err,
		)
	}
	if jobIDValue <= 0 {
		return 0, fmt.Errorf("invalid job ID %d for pod %v: cannot perform update", jobIDValue, klog.KObj(pod))
	}
	jobID := int32(jobIDValue)

	job := new(api.V0044JobInfo)
	jobDesc, err := r.buildJobDesc(component, true)
	if err != nil {
		return 0, err
	}
	jobSubmit := api.V0044JobSubmitReq{
		Job: ptr.To(jobDesc),
	}

	if last, ok := r.lastUpdate.Get(jobID); ok && reflect.DeepEqual(last, jobDesc) {
		logger.V(4).Info("external job unchanged, skipping update", "jobId", jobID)
		return jobID, nil
	}

	job.JobId = ptr.To(jobID)
	if err := r.Update(ctx, job, *jobSubmit.Job); err != nil {
		// Slurm may have applied the update before the error; don't trust the old entry.
		r.lastUpdate.Remove(jobID)
		logger.Error(err, "could not update external job", "pod", klog.KObj(pod))
		return 0, err
	}
	r.lastUpdate.Add(jobID, jobDesc)

	return jobID, nil
}

// submitJob will create or update an external job in Slurm.
func (r *realSlurmControl) submitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) ([]int32, error) {
	logger := klog.FromContext(ctx)
	job := new(api.V0044JobInfo)
	var jobSubmit api.V0044JobSubmitReq

	var jobDescList []api.V0044JobDescMsg
	for _, component := range slurmJobIR.Components {
		jobDesc, err := r.buildJobDesc(component, false)
		if err != nil {
			return []int32{}, err
		}
		jobDescList = append(jobDescList, jobDesc)
	}

	if len(jobDescList) == 1 {
		jobSubmit = api.V0044JobSubmitReq{Job: ptr.To(jobDescList[0])}
	} else {
		jobSubmit = api.V0044JobSubmitReq{Jobs: ptr.To(jobDescList)}
	}

	if err := r.Create(ctx, job, jobSubmit); err != nil {
		logger.Error(err, "could not create external job", "pod", klog.KObj(pod))
		return []int32{}, err
	}

	jobID := ptr.Deref(job.JobId, 0)
	if jobID == 0 {
		err := errors.New("job submission returned invalid jobid")
		return nil, err
	}

	jobIDs := []int32{}
	if job.HetJobIdSet != nil && *job.HetJobIdSet != "" {
		hetJobIDs, err := cpuset.Parse(*job.HetJobIdSet)
		if err != nil {
			return nil, err
		}
		for _, id := range hetJobIDs.List() {
			jobIDs = append(jobIDs, int32(id)) // nolint:gosec // Value bounded by Slurm
		}
	} else {
		jobIDs = append(jobIDs, jobID)
	}

	return jobIDs, nil
}

func (r *realSlurmControl) GetNodeNames(ctx context.Context, partition *string) ([]string, error) {
	list := &api.V0044NodeObjectList{}
	if err := r.List(ctx, list, &client.ListOptions{SkipCache: true}); err != nil {
		return nil, err
	}
	partitionNames := strings.Split(ptr.Deref(partition, r.partition), ",")
	for i := range partitionNames {
		partitionNames[i] = strings.TrimSpace(partitionNames[i])
	}
	nodeNames := make([]string, 0, len(list.Items))
	for _, node := range list.Items {
		partitions := ptr.Deref(node.Partitions, api.V0044CsvString{})
		for _, partitionName := range partitionNames {
			if slices.Contains(partitions, partitionName) {
				nodeNames = append(nodeNames, ptr.Deref(node.Name, ""))
				break
			}
		}
	}
	return nodeNames, nil
}

func (r *realSlurmControl) buildJobDesc(jobComponent slurmjobir.SlurmJobComponent, update bool) (api.V0044JobDescMsg, error) {
	extInfo := externaljobinfo.ExternalJobInfo{}
	for _, p := range jobComponent.Pods.Items {
		extInfo.Pods = append(extInfo.Pods, externaljobinfo.PodKey(&p))
	}
	constraints, err := gresCompatibilityConstraint(jobComponent.JobInfo.Constraints)
	if err != nil {
		return api.V0044JobDescMsg{}, err
	}
	excludedNodes := append(api.V0044CsvString{}, jobComponent.JobInfo.ExcNodes...)

	jobDesc := api.V0044JobDescMsg{
		Account:                 jobComponent.JobInfo.Account,
		AdminComment:            ptr.To(extInfo.ToString()),
		CpusPerTask:             jobComponent.JobInfo.CpuPerTask,
		Constraints:             constraints,
		CurrentWorkingDirectory: ptr.To("/tmp"),
		Flags: &[]api.V0044JobDescMsgFlags{
			api.V0044JobDescMsgFlagsEXTERNALJOB,
		},
		GroupId:       jobComponent.JobInfo.GroupId,
		Licenses:      jobComponent.JobInfo.Licenses,
		MaximumNodes:  jobComponent.JobInfo.MaxNodes,
		McsLabel:      ptr.To(r.mcsLabel),
		MemoryPerNode: &api.V0044Uint64NoValStruct{Set: ptr.To(false)},
		MinimumNodes:  jobComponent.JobInfo.MinNodes,
		Name:          jobComponent.JobInfo.JobName,
		Nodes:         ptr.To(strconv.Itoa(len(jobComponent.Pods.Items))),
		Priority:      &api.V0044Uint32NoValStruct{Set: ptr.To(false)},
		Qos:           jobComponent.JobInfo.QOS,
		Reservation:   jobComponent.JobInfo.Reservation,
		TasksPerNode:  jobComponent.JobInfo.TasksPerNode,
		TimeLimit:     &api.V0044Uint32NoValStruct{Set: ptr.To(false)},
		TresPerNode:   jobComponent.JobInfo.Gres,
		UserId:        jobComponent.JobInfo.UserId,
		Wckey:         jobComponent.JobInfo.Wckey,
	}

	// Set sharing only at submission. Slurm treats nonzero shared updates as
	// oversubscribe and clears whole_node, including the MCS isolation flag.
	// Omitting shared preserves the existing allocation's sharing mode.
	if !update {
		jobDesc.Shared = sharedFromExclusiveAnnotation(&jobComponent)
	}

	if len(excludedNodes) == 0 && !update {
		jobDesc.ExcludedNodes = nil
	} else {
		jobDesc.ExcludedNodes = &excludedNodes
	}

	if jobComponent.JobInfo.MemPerNode != nil {
		jobDesc.MemoryPerNode = &api.V0044Uint64NoValStruct{
			Infinite: ptr.To(false),
			Number:   jobComponent.JobInfo.MemPerNode,
			Set:      ptr.To(true),
		}
	}

	if jobComponent.JobInfo.Priority != nil {
		jobDesc.Priority = &api.V0044Uint32NoValStruct{
			Infinite: ptr.To(false),
			Number:   jobComponent.JobInfo.Priority,
			Set:      ptr.To(true),
		}
	}

	if jobComponent.JobInfo.Partition == nil {
		jobDesc.Partition = &r.partition
	} else {
		jobDesc.Partition = jobComponent.JobInfo.Partition
	}

	if jobComponent.JobInfo.TimeLimit != nil {
		jobDesc.TimeLimit = &api.V0044Uint32NoValStruct{
			Infinite: ptr.To(false),
			Number:   jobComponent.JobInfo.TimeLimit,
			Set:      ptr.To(true),
		}
	}

	return jobDesc, nil
}

// GetResources will return the resources used by a node for a given JobId
func (r *realSlurmControl) GetResources(ctx context.Context, pod *corev1.Pod, nodeName string) (*NodeResources, error) {
	logger := klog.FromContext(ctx)

	nodes := &api.V0044NodeResourceLayoutListObject{}
	jobId := object.ObjectKey(pod.Labels[wellknown.LabelExternalJobId])
	if jobId == "" {
		return &NodeResources{}, nil
	}

	err := r.Get(ctx, jobId, nodes, &client.GetOptions{SkipCache: true})
	if err != nil {
		logger.Error(err, "could not get node resource layout for pod", "pod", klog.KObj(pod))
		return nil, err
	}
	for _, n := range *nodes {
		if n.Node != nodeName {
			continue
		}
		nodeExtra, err := r.getNodeExtra(ctx, nodeName)
		if err != nil {
			logger.Error(err, "could not get Slurm node Extra", "node", nodeName)
			return nil, err
		}
		nodeOut := NodeResources{
			Node:           n.Node,
			NodeExtra:      nodeExtra,
			SocketsPerNode: ptr.Deref(n.SocketsPerNode, 0),
			CoresPerSocket: ptr.Deref(n.CoresPerSocket, 0),
			MemAlloc:       ptr.Deref(n.MemAlloc, 0),
			CoreBitmap:     ptr.Deref(n.CoreBitmap, ""),
			Channel:        ptr.Deref(ptr.Deref(n.Channel, api.V0044Uint32NoValStruct{}).Number, 0),
			Gres:           make([]GresLayout, len(ptr.Deref(n.Gres, api.V0044NodeGresLayoutList{}))),
		}
		for i, g := range ptr.Deref(n.Gres, api.V0044NodeGresLayoutList{}) {
			nodeOut.Gres[i] = GresLayout{
				Name:  g.Name,
				Type:  ptr.Deref(g.Type, ""),
				Count: ptr.Deref(g.Count, 0),
				Index: ptr.Deref(g.Index, ""),
			}
		}
		return &nodeOut, nil
	}
	return &NodeResources{}, nil
}

func (r *realSlurmControl) getNodeExtra(ctx context.Context, nodeName string) (string, error) {
	node := &api.V0044Node{}
	if err := r.Get(ctx, object.ObjectKey(nodeName), node, &client.GetOptions{SkipCache: true}); err != nil {
		return "", err
	}
	return ptr.Deref(node.Extra, ""), nil
}

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(slurmClient client.Client, jobInformer client.InformerCache, mcsLabel string, partition string) (SlurmControlInterface, error) {
	index, err := slurmjob.NewPodIndex(slurmClient, jobInformer)
	if err != nil {
		return nil, err
	}
	return &realSlurmControl{
		Client:    slurmClient,
		PodIndex:  index,
		mcsLabel:  mcsLabel,
		partition: partition,
		// Fixed cap; make it configurable if pending-job counts outgrow it.
		lastUpdate: lru.New(10000),
	}, nil
}
