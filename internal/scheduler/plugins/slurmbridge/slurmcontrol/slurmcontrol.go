// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	v0043 "github.com/SlinkyProject/slurm-client/api/v0043"
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
	DeleteJob(ctx context.Context, pod *corev1.Pod) error
	GetJobsForPods(ctx context.Context) (*map[string]PlaceholderJob, error)
	GetJob(ctx context.Context, pod *corev1.Pod) (*PlaceholderJob, error)
	SubmitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error)
	UpdateJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error)
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
	mcsLabel  string
	partition string
}

// DeleteSlurmJob will delete a placeholder job
func (r *realSlurmControl) DeleteJob(ctx context.Context, pod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	job := &slurmtypes.V0043JobInfo{}
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

	jobs := &slurmtypes.V0043JobInfoList{}

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

	job := &slurmtypes.V0043JobInfo{}
	jobId := object.ObjectKey(pod.Labels[wellknown.LabelPlaceholderJobId])
	if jobId == "" {
		return nil, nil
	}

	err := r.Get(ctx, jobId, job)
	if err != nil {
		if err.Error() == http.StatusText(http.StatusNotFound) {
			return nil, nil
		}
		logger.Error(err, "could not get job for pod", "pod", klog.KObj(pod))
		return nil, err
	}

	if job.GetStateAsSet().HasAny(v0043.V0043JobInfoJobStateCANCELLED, v0043.V0043JobInfoJobStateCOMPLETED) {
		return nil, nil
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

// submitJob will create or update a placeholder job Slurm.
func (r *realSlurmControl) submitJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR, update bool) (int32, error) {
	logger := klog.FromContext(ctx)
	phInfo := placeholderinfo.PlaceholderInfo{}
	for _, p := range slurmJobIR.Pods.Items {
		phInfo.Pods = append(phInfo.Pods, p.Namespace+"/"+p.Name)
	}
	job := &slurmtypes.V0043JobInfo{}
	jobSubmit := v0043.V0043JobSubmitReq{
		Job: &v0043.V0043JobDescMsg{
			Account:                 slurmJobIR.JobInfo.Account,
			AdminComment:            ptr.To(phInfo.ToString()),
			CpusPerTask:             slurmJobIR.JobInfo.CpuPerTask,
			Constraints:             slurmJobIR.JobInfo.Constraints,
			CurrentWorkingDirectory: ptr.To("/tmp"),
			Environment: &v0043.V0043StringArray{
				"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin",
			},
			Flags: &[]v0043.V0043JobDescMsgFlags{
				v0043.V0043JobDescMsgFlagsEXTERNALJOB,
			},
			Licenses:     slurmJobIR.JobInfo.Licenses,
			MaximumNodes: slurmJobIR.JobInfo.MaxNodes,
			McsLabel:     ptr.To(r.mcsLabel),
			MemoryPerNode: func() *v0043.V0043Uint64NoValStruct {
				if slurmJobIR.JobInfo.MemPerNode != nil {
					return &v0043.V0043Uint64NoValStruct{
						Infinite: ptr.To(false),
						Number:   slurmJobIR.JobInfo.MemPerNode,
						Set:      ptr.To(true),
					}
				} else {
					return &v0043.V0043Uint64NoValStruct{Set: ptr.To(false)}
				}
			}(),
			MinimumNodes: slurmJobIR.JobInfo.MinNodes,
			Name:         slurmJobIR.JobInfo.JobName,
			Partition: func() *string {
				if slurmJobIR.JobInfo.Partition == nil {
					return &r.partition
				} else {
					return slurmJobIR.JobInfo.Partition
				}
			}(),
			Qos:         slurmJobIR.JobInfo.QOS,
			Reservation: slurmJobIR.JobInfo.Reservation,
			// SharedNone is effectively Exclusive
			Shared:       &[]v0043.V0043JobDescMsgShared{v0043.V0043JobDescMsgSharedNone},
			TasksPerNode: slurmJobIR.JobInfo.TasksPerNode,
			TimeLimit: func() *v0043.V0043Uint32NoValStruct {
				if slurmJobIR.JobInfo.TimeLimit != nil {
					return &v0043.V0043Uint32NoValStruct{
						Infinite: ptr.To(false),
						Number:   slurmJobIR.JobInfo.TimeLimit,
						Set:      ptr.To(true),
					}
				} else {
					return &v0043.V0043Uint32NoValStruct{Set: ptr.To(false)}
				}
			}(),
			Wckey: slurmJobIR.JobInfo.Wckey,
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

var _ SlurmControlInterface = &realSlurmControl{}

func NewControl(client client.Client, mcsLabel string, partition string) SlurmControlInterface {
	return &realSlurmControl{
		Client:    client,
		mcsLabel:  mcsLabel,
		partition: partition,
	}
}
