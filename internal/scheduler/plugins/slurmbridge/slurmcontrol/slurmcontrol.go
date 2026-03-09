// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/placeholderinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type PlaceholderJob struct {
	JobId int32
	Nodes string
}

type SlurmControlInterface interface {
	GetResources(ctx context.Context, pod *corev1.Pod, nodeName string) (*NodeResources, error)
	DeleteJob(ctx context.Context, pod *corev1.Pod) error
	GetJobsForPods(ctx context.Context) (*map[string]PlaceholderJob, error)
	GetJob(ctx context.Context, pod *corev1.Pod) (*PlaceholderJob, error)
	SubmitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error)
	UpdateJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error)
	IsSlurmNode(ctx context.Context, node string) (bool, error)
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
	mcsLabel  string
	partition string
}

type NodeResources struct {
	Node           string
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

// DeleteSlurmJob will delete a placeholder job
func (r *realSlurmControl) DeleteJob(ctx context.Context, pod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	job := &slurmtypes.V0044JobInfo{}
	jobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelPlaceholderJobId])
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
func (r *realSlurmControl) GetJobsForPods(ctx context.Context) (*map[string]PlaceholderJob, error) {
	logger := klog.FromContext(ctx)

	jobs := &slurmtypes.V0044JobInfoList{}

	err := r.List(ctx, jobs)
	if err != nil {
		logger.Error(err, "could not list jobs")
		return nil, err
	}
	podToJob := make(map[string]PlaceholderJob)
	for _, j := range jobs.Items {
		phInfo := placeholderinfo.PlaceholderInfo{}
		if err := placeholderinfo.ParseIntoPlaceholderInfo(j.AdminComment, &phInfo); err == nil {
			for _, pod := range phInfo.Pods {
				podToJob[pod] = PlaceholderJob{
					JobId: *j.JobId,
					Nodes: *j.Nodes,
				}
			}
		}
	}

	return &podToJob, nil
}

// GetJob will check if a placeholder job has been created for a given pod
func (r *realSlurmControl) GetJob(ctx context.Context, pod *corev1.Pod) (*PlaceholderJob, error) {
	logger := klog.FromContext(ctx)
	jobOut := PlaceholderJob{}

	job := &slurmtypes.V0044JobInfo{}
	jobId := object.ObjectKey(pod.Labels[wellknown.LabelPlaceholderJobId])
	if jobId == "" {
		return &jobOut, nil
	}

	err := r.Get(ctx, jobId, job)
	if err != nil {
		if err.Error() == http.StatusText(http.StatusNotFound) {
			return &jobOut, nil
		}
		logger.Error(err, "could not get job for pod", "pod", klog.KObj(pod))
		return nil, err
	}

	if job.GetStateAsSet().HasAny(api.V0044JobInfoJobStateCANCELLED, api.V0044JobInfoJobStateCOMPLETED) {
		return &jobOut, nil
	}
	logger.V(5).Info("found matching job")
	jobOut.JobId = *job.JobId
	jobOut.Nodes = *job.Nodes
	return &jobOut, nil
}

// SubmitJob submits a placeholder job to Slurm for a node placement decision. The
// placeholder job is later used to determine which node to bind a k8s pod to.
func (r *realSlurmControl) SubmitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error) {
	return r.submitJob(ctx, pod, slurmJobIR, false)
}

// UpdateJob updates a placeholder job
func (r *realSlurmControl) UpdateJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error) {
	return r.submitJob(ctx, pod, slurmJobIR, true)
}

// sharedForJob returns the shared policy for the job
func sharedForJob(slurmJobIR *slurmjobir.SlurmJobIR) *[]api.V0044JobDescMsgShared {
	if len(slurmJobIR.Pods.Items) != 1 || slurmJobIR.JobInfo.Shared == nil {
		return &[]api.V0044JobDescMsgShared{api.V0044JobDescMsgSharedNone}
	}
	shared, ok := map[string]api.V0044JobDescMsgShared{
		"none": api.V0044JobDescMsgSharedNone,
		"user": api.V0044JobDescMsgSharedUser,
	}[*slurmJobIR.JobInfo.Shared]

	if !ok {
		shared = api.V0044JobDescMsgSharedNone
	}
	return &[]api.V0044JobDescMsgShared{shared}
}

// submitJob will create or update a placeholder job Slurm.
func (r *realSlurmControl) submitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR, update bool) (int32, error) {
	logger := klog.FromContext(ctx)
	phInfo := placeholderinfo.PlaceholderInfo{}
	for _, p := range slurmJobIR.Pods.Items {
		phInfo.Pods = append(phInfo.Pods, p.Namespace+"/"+p.Name)
	}
	job := &slurmtypes.V0044JobInfo{}
	jobSubmit := api.V0044JobSubmitReq{
		Job: &api.V0044JobDescMsg{
			Account:                 slurmJobIR.JobInfo.Account,
			AdminComment:            ptr.To(phInfo.ToString()),
			CpusPerTask:             slurmJobIR.JobInfo.CpuPerTask,
			Constraints:             slurmJobIR.JobInfo.Constraints,
			CurrentWorkingDirectory: ptr.To("/tmp"),
			Flags: &[]api.V0044JobDescMsgFlags{
				api.V0044JobDescMsgFlagsEXTERNALJOB,
			},
			GroupId:      slurmJobIR.JobInfo.GroupId,
			Licenses:     slurmJobIR.JobInfo.Licenses,
			MaximumNodes: slurmJobIR.JobInfo.MaxNodes,
			McsLabel:     ptr.To(r.mcsLabel),
			MemoryPerNode: func() *api.V0044Uint64NoValStruct {
				if slurmJobIR.JobInfo.MemPerNode != nil {
					return &api.V0044Uint64NoValStruct{
						Infinite: ptr.To(false),
						Number:   slurmJobIR.JobInfo.MemPerNode,
						Set:      ptr.To(true),
					}
				} else {
					return &api.V0044Uint64NoValStruct{Set: ptr.To(false)}
				}
			}(),
			MinimumNodes:  slurmJobIR.JobInfo.MinNodes,
			Name:          slurmJobIR.JobInfo.JobName,
			Nodes:         ptr.To(strconv.Itoa(len(slurmJobIR.Pods.Items))),
			RequiredNodes: ptr.To(api.V0044CsvString(slurmJobIR.JobInfo.Nodes)),
			Partition: func() *string {
				if slurmJobIR.JobInfo.Partition == nil {
					return &r.partition
				} else {
					return slurmJobIR.JobInfo.Partition
				}
			}(),
			Qos:          slurmJobIR.JobInfo.QOS,
			Reservation:  slurmJobIR.JobInfo.Reservation,
			Shared:       sharedForJob(slurmJobIR),
			TasksPerNode: slurmJobIR.JobInfo.TasksPerNode,
			TimeLimit: func() *api.V0044Uint32NoValStruct {
				if slurmJobIR.JobInfo.TimeLimit != nil {
					return &api.V0044Uint32NoValStruct{
						Infinite: ptr.To(false),
						Number:   slurmJobIR.JobInfo.TimeLimit,
						Set:      ptr.To(true),
					}
				} else {
					return &api.V0044Uint32NoValStruct{Set: ptr.To(false)}
				}
			}(),
			TresPerNode: slurmJobIR.JobInfo.Gres,
			UserId:      slurmJobIR.JobInfo.UserId,
			Wckey:       slurmJobIR.JobInfo.Wckey,
		},
	}
	if !update {
		if err := r.Create(ctx, job, jobSubmit); err != nil {
			logger.Error(err, "could not create placeholder job", "pod", klog.KObj(pod))
			return 0, err
		}
	} else {
		job.JobId = ptr.To(slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelPlaceholderJobId]))
		if err := r.Update(ctx, job, *jobSubmit.Job); err != nil {
			logger.Error(err, "could not update placeholder job", "pod", klog.KObj(pod))
			return 0, err
		}
	}
	return ptr.Deref(job.JobId, 0), nil
}

func (r *realSlurmControl) IsSlurmNode(ctx context.Context, nodeName string) (bool, error) {
	logger := klog.FromContext(ctx)

	node := &slurmtypes.V0044Node{}
	nodeKey := object.ObjectKey(nodeName)

	err := r.Get(ctx, nodeKey, node)
	if err != nil {
		if err.Error() == http.StatusText(http.StatusNotFound) {
			return false, nil
		}
		logger.Error(err, "could not get slurm node", "pod", nodeName)
		return false, err
	}
	return true, nil
}

// GetResources will return the resources used by a node for a given JobId
func (r *realSlurmControl) GetResources(ctx context.Context, pod *corev1.Pod, nodeName string) (*NodeResources, error) {
	logger := klog.FromContext(ctx)

	nodes := &slurmtypes.V0044NodeResourceLayout{}
	jobId := object.ObjectKey(pod.Labels[wellknown.LabelPlaceholderJobId])
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
		nodeOut := NodeResources{
			Node:           n.Node,
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

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(client client.Client, mcsLabel string, partition string) SlurmControlInterface {
	return &realSlurmControl{
		Client:    client,
		mcsLabel:  mcsLabel,
		partition: partition,
	}
}
