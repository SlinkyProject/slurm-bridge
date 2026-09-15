// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	slurmerrors "github.com/SlinkyProject/slurm-client/pkg/errors"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/externaljobinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmconstraint"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type ExternalJob struct {
	JobId        int32
	HetJobId     int32
	HetJobOffset int32
	Nodes        string
	Pending      bool
}

func externalJobFromJobInfo(job *slurmtypes.V0044JobInfo) (ExternalJob, error) {
	if job == nil {
		return ExternalJob{}, errors.New("cannot convert nil Slurm job")
	}

	extJob := ExternalJob{
		JobId:   ptr.Deref(job.JobId, 0),
		Nodes:   ptr.Deref(job.Nodes, ""),
		Pending: job.GetStateAsSet().Has(api.V0044JobInfoJobStatePENDING),
	}

	return validateExternalJobID(job, extJob)
}

func validateExternalJobID(job *slurmtypes.V0044JobInfo, extJob ExternalJob) (ExternalJob, error) {
	if extJob.JobId <= 0 {
		return ExternalJob{}, errors.New("invalid or unset job ID")
	}

	if job.HetJobId == nil || !ptr.Deref(job.HetJobId.Set, false) {
		return extJob, nil
	}
	if job.HetJobId.Number == nil {
		return ExternalJob{}, errors.New("malformed het-job: het job ID is set but its value is missing")
	}
	if job.HetJobOffset == nil {
		return ExternalJob{}, errors.New("malformed het-job: het job ID is set but het job offset is missing")
	}
	if !ptr.Deref(job.HetJobOffset.Set, false) {
		return ExternalJob{}, errors.New("malformed het-job: het job ID is set but het job offset is unset")
	}
	if job.HetJobOffset.Number == nil {
		return ExternalJob{}, errors.New("malformed het-job: het job offset is set but its value is missing")
	}

	extJob.HetJobId = *job.HetJobId.Number
	extJob.HetJobOffset = *job.HetJobOffset.Number

	if extJob.HetJobId == 0 {
		if extJob.HetJobOffset != 0 {
			return ExternalJob{}, errors.New("invalid het job offset: homogeneous job has non-zero offset")
		}
		return extJob, nil
	}
	if extJob.HetJobId < 0 {
		return ExternalJob{}, fmt.Errorf("invalid het job ID %d: value must be positive", extJob.HetJobId)
	}
	if extJob.HetJobOffset < 0 {
		return ExternalJob{}, errors.New("invalid het job offset: value is negative")
	}
	if extJob.HetJobId != extJob.JobId && extJob.HetJobOffset == 0 {
		return ExternalJob{}, errors.New("invalid het job offset: non-leader component has offset zero")
	}

	return extJob, nil
}

func isActiveJob(job *slurmtypes.V0044JobInfo) bool {
	state := job.GetStateAsSet()
	return state.Len() == 0 || !state.HasAny(
		api.V0044JobInfoJobStateCANCELLED,
		api.V0044JobInfoJobStateCOMPLETED,
	)
}

type SlurmControlInterface interface {
	GetResources(ctx context.Context, pod *corev1.Pod, nodeName string) (*NodeResources, error)
	DeleteJob(ctx context.Context, pod *corev1.Pod) error
	GetJobsForPods(ctx context.Context) (*map[string]ExternalJob, error)
	GetJob(ctx context.Context, pod *corev1.Pod) (*ExternalJob, error)
	GetNodeNames(ctx context.Context, partition *string) ([]string, error)
	SubmitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) ([]int32, error)
	UpdateJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error)
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
	mcsLabel  string
	partition string
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
	job := &slurmtypes.V0044JobInfo{}
	jobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
	if jobId == 0 {
		return nil
	}
	job.JobId = &jobId
	if err := r.Delete(ctx, job); err != nil {
		logger.Error(err, "failed to delete Slurm job", "jobId", jobId)
		return err
	}
	return nil
}

// GetJobsForPods will get a list of all slurm jobs and translate them into a podToJob
func (r *realSlurmControl) GetJobsForPods(ctx context.Context) (*map[string]ExternalJob, error) {
	logger := klog.FromContext(ctx)

	jobs := &slurmtypes.V0044JobInfoList{}

	err := r.List(ctx, jobs)
	if err != nil {
		logger.Error(err, "could not list jobs")
		return nil, err
	}
	podToJob := make(map[string]ExternalJob)
	for i := range jobs.Items {
		job := &jobs.Items[i]
		extInfo := externaljobinfo.ExternalJobInfo{}
		if err := externaljobinfo.ParseIntoExternalJobInfo(job.AdminComment, &extInfo); err != nil {
			continue
		}
		extJob, err := externalJobFromJobInfo(job)
		if err != nil {
			logger.Error(err, "skipping malformed external job", "jobId", ptr.Deref(job.JobId, 0))
			continue
		}
		for _, pod := range extInfo.Pods {
			podToJob[pod] = extJob
		}
	}

	return &podToJob, nil
}

// GetJob will check if an external job has been created for a given pod
func (r *realSlurmControl) GetJob(ctx context.Context, pod *corev1.Pod) (*ExternalJob, error) {
	logger := klog.FromContext(ctx)
	jobOut := ExternalJob{}

	job := &slurmtypes.V0044JobInfo{}
	jobIDLabel := pod.Labels[wellknown.LabelExternalJobId]
	if jobIDLabel == "" {
		return &jobOut, nil
	}

	err := r.Get(ctx, object.ObjectKey(jobIDLabel), job)
	if err != nil {
		if errors.Is(err, slurmerrors.ErrNotFound) {
			return &jobOut, nil
		}
		logger.Error(err, "could not get job for pod", "pod", klog.KObj(pod))
		return nil, err
	}

	expectedJobID := slurmjobir.ParseSlurmJobId(jobIDLabel)
	if ptr.Deref(job.JobId, 0) != expectedJobID {
		var found bool
		job, found, err = r.findJobByID(ctx, expectedJobID)
		if err != nil {
			return nil, err
		}
		if !found {
			return &jobOut, nil
		}
	}

	if !isActiveJob(job) {
		return &jobOut, nil
	}
	logger.V(5).Info("found matching job")
	jobOut, err = externalJobFromJobInfo(job)
	if err != nil {
		return nil, err
	}

	return &jobOut, nil
}

func (r *realSlurmControl) findJobByID(ctx context.Context, jobID int32) (*slurmtypes.V0044JobInfo, bool, error) {
	jobs := &slurmtypes.V0044JobInfoList{}
	if err := r.List(ctx, jobs); err != nil {
		return nil, false, err
	}
	for i := range jobs.Items {
		if ptr.Deref(jobs.Items[i].JobId, 0) == jobID {
			return &jobs.Items[i], true, nil
		}
	}
	return nil, false, nil
}

func (r *realSlurmControl) ListJobs(ctx context.Context) ([]ExternalJob, error) {
	logger := klog.FromContext(ctx)
	jobsOut := []ExternalJob{}

	jobs := &slurmtypes.V0044JobInfoList{}

	err := r.List(ctx, jobs)
	if err != nil {
		logger.Error(err, "could not list jobs")
		return nil, err
	}

	for i := range jobs.Items {
		if !isActiveJob(&jobs.Items[i]) {
			continue
		}
		extJob, err := externalJobFromJobInfo(&jobs.Items[i])
		if err != nil {
			return nil, err
		}
		jobsOut = append(jobsOut, extJob)
	}

	return jobsOut, nil
}

// SubmitJob submits an external job to Slurm for a node placement decision. The
// external job is later used to determine which node to bind a k8s pod to.
func (r *realSlurmControl) SubmitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) ([]int32, error) {
	if err := slurmJobIR.Validate(); err != nil {
		return []int32{}, err
	}
	if slurmJobIR.IsHetJob() {
		return []int32{}, errors.New("multi-component Slurm jobs are not supported")
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

	job := new(slurmtypes.V0044JobInfo)
	jobDesc, err := r.buildJobDesc(component, true)
	if err != nil {
		return 0, err
	}
	jobSubmit := api.V0044JobSubmitReq{
		Job: ptr.To(jobDesc),
	}

	job.JobId = ptr.To(jobID)
	if err := r.Update(ctx, job, *jobSubmit.Job); err != nil {
		logger.Error(err, "could not update external job", "pod", klog.KObj(pod))
		return 0, err
	}

	return jobID, nil
}

// submitJob will create or update an external job in Slurm.
func (r *realSlurmControl) submitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) ([]int32, error) {
	logger := klog.FromContext(ctx)
	job := new(slurmtypes.V0044JobInfo)
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

	// TODO This will be refactored to get and parse component jobids
	// instead of inferring them
	baseJobID := ptr.Deref(job.JobId, 0)
	jobIDs := make([]int32, len(slurmJobIR.Components))
	for i := range jobIDs {
		jobIDs[i] = baseJobID + int32(i)
	}
	return jobIDs, nil
}

func (r *realSlurmControl) GetNodeNames(ctx context.Context, partition *string) ([]string, error) {
	list := &slurmtypes.V0044NodeList{}
	if err := r.List(ctx, list); err != nil {
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
		extInfo.Pods = append(extInfo.Pods, p.Namespace+"/"+p.Name)
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
		Shared:        sharedFromExclusiveAnnotation(&jobComponent),
		TasksPerNode:  jobComponent.JobInfo.TasksPerNode,
		TimeLimit:     &api.V0044Uint32NoValStruct{Set: ptr.To(false)},
		TresPerNode:   jobComponent.JobInfo.Gres,
		UserId:        jobComponent.JobInfo.UserId,
		Wckey:         jobComponent.JobInfo.Wckey,
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

	nodes := &slurmtypes.V0044NodeResourceLayout{}
	jobId := object.ObjectKey(pod.Labels[wellknown.LabelExternalJobId])
	if jobId == "" {
		return &NodeResources{}, nil
	}

	err := r.Get(ctx, jobId, nodes)
	if err != nil {
		logger.Error(err, "could not get node resource layout for pod", "pod", klog.KObj(pod))
		return nil, err
	}
	for _, n := range nodes.V0044NodeResourceLayoutList {
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
	node := &slurmtypes.V0044Node{}
	if err := r.Get(ctx, object.ObjectKey(nodeName), node); err != nil {
		return "", err
	}
	return ptr.Deref(node.Extra, ""), nil
}

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(client client.Client, mcsLabel string, partition string) SlurmControlInterface {
	return &realSlurmControl{
		Client:    client,
		mcsLabel:  mcsLabel,
		partition: partition,
	}
}
