// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
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
	GetResourcesForNodes(ctx context.Context, pod *corev1.Pod, nodeNames []string) (map[string]*NodeResources, error)
	DeleteJob(ctx context.Context, pod *corev1.Pod) error
	GetJobsForPods(ctx context.Context, pods ...*corev1.Pod) (*map[string]ExternalJob, error)
	VerifyJobsForPods(ctx context.Context, candidates map[string]ExternalJob, pods ...*corev1.Pod) (*map[string]ExternalJob, error)
	GetJob(ctx context.Context, pod *corev1.Pod) (*ExternalJob, error)
	GetAllocation(ctx context.Context, pod *corev1.Pod, ir *slurmjobir.SlurmJobIR) ([]ExternalJob, error)
	GetNodeNames(ctx context.Context, partition *string) ([]string, error)
	ReleaseJob(ctx context.Context, jobID int32, priority *int32) error
	UpdateJob(ctx context.Context, pod *corev1.Pod, slurmJobIR *slurmjobir.SlurmJobIR) (int32, error)
}

// RealPodControl is the default implementation of SlurmControlInterface.
type realSlurmControl struct {
	client.Client
	*slurmjob.PodIndex
	mcsLabel  string
	partition string
	nodes     client.InformerCache
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

// DeleteJob releases an external job, including one already canceled or gone.
func (r *realSlurmControl) DeleteJob(ctx context.Context, pod *corev1.Pod) error {
	logger := klog.FromContext(ctx)
	job := &api.V0044JobInfo{}
	jobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalJobId])
	if jobId == 0 {
		return nil
	}
	hetJobId := slurmjobir.ParseSlurmJobId(pod.Labels[wellknown.LabelExternalHetJobId])
	if hetJobId != 0 {
		jobId = hetJobId
	}
	job.JobId = &jobId
	r.lastUpdate.Remove(jobId)
	if err := r.Delete(ctx, job, &client.DeleteOptions{SkipReadAfterDelete: true}); err != nil {
		// Cancellation may have succeeded on an earlier attempt whose pod
		// metadata cleanup failed. Confirm the allocation is gone before
		// allowing cleanup to continue despite a cancellation error.
		externalJob, lookupErr := r.GetJob(ctx, pod)
		if lookupErr == nil && externalJob.JobId == 0 {
			return nil
		}
		if lookupErr != nil {
			err = errors.Join(err, fmt.Errorf("confirm Slurm job release: %w", lookupErr))
		}
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

// JobRegistrar submits a held allocation and returns its accepted leader ID.
// Component IDs are subsequently resolved through the job indexes.
type JobRegistrar interface {
	RegisterJob(context.Context, *slurmjobir.SlurmJobIR) (int32, error)
}

// ErrRegistrationNotSubmitted marks failures before a submission reaches the client.
var ErrRegistrationNotSubmitted = errors.New("registration was not submitted")

func NewRegistrar(c client.Client, mcsLabel, partition string) JobRegistrar {
	return &realSlurmControl{Client: c, mcsLabel: mcsLabel, partition: partition}
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
	jobDesc, err := r.buildJobDesc(slurmJobIR.RootPOM, component, true)
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
	// PostFilter ends this scheduling attempt after updating eligibility. The
	// allocation observer retries when Slurm assigns nodes; no readback is needed.
	if err := r.Update(ctx, job, *jobSubmit.Job, &client.UpdateOptions{SkipReadAfterUpdate: true}); err != nil {
		// Slurm may have applied the update before the error; don't trust the old entry.
		r.lastUpdate.Remove(jobID)
		logger.Error(err, "could not update external job", "pod", klog.KObj(pod))
		return 0, err
	}
	r.lastUpdate.Add(jobID, jobDesc)

	return jobID, nil
}

// RegisterJob waits for Slurm's create response and returns the accepted job ID.
func (r *realSlurmControl) RegisterJob(ctx context.Context, ir *slurmjobir.SlurmJobIR) (int32, error) {
	if err := ir.Validate(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrRegistrationNotSubmitted, err)
	}
	job := new(api.V0044JobInfo)
	var req api.V0044JobSubmitReq
	var descriptions []api.V0044JobDescMsg
	for _, component := range ir.Components {
		component.JobInfo.Hold = ptr.To(true)
		desc, err := r.buildJobDesc(ir.RootPOM, component, false)
		if err != nil {
			return 0, fmt.Errorf("%w: %w", ErrRegistrationNotSubmitted, err)
		}
		descriptions = append(descriptions, desc)
	}
	if len(descriptions) == 1 {
		req.Job = &descriptions[0]
	} else {
		req.Jobs = &descriptions
	}
	err := r.Create(ctx, job, req, &client.CreateOptions{SkipReadAfterCreate: true})
	id := ptr.Deref(job.JobId, 0)
	if err != nil {
		return id, err
	}
	if id <= 0 {
		return 0, errors.New("registration returned an invalid job ID")
	}
	return id, nil
}

// ReleaseJob is called after every component's eligibility has been updated.
// Slurm maps hold and priority to the same field: they must be separate requests.
// Wait for each write's acknowledgement, but leave observing the allocation to
// the allocation observer and the next scheduling attempt.
func (r *realSlurmControl) ReleaseJob(ctx context.Context, id int32, priority *int32) error {
	job := &api.V0044JobInfo{JobId: ptr.To(id)}
	if err := r.Update(ctx, job, api.V0044JobDescMsg{Hold: ptr.To(false)}, &client.UpdateOptions{SkipReadAfterUpdate: true}); err != nil {
		return err
	}
	if priority != nil {
		return r.Update(ctx, job, api.V0044JobDescMsg{Priority: &api.V0044Uint32NoValStruct{Set: ptr.To(true), Number: priority, Infinite: ptr.To(false)}}, &client.UpdateOptions{SkipReadAfterUpdate: true})
	}
	return nil
}

// ExternalJobInfoFor records the allocation identity Slurm stores for one component.
func ExternalJobInfoFor(root metav1.PartialObjectMetadata, component slurmjobir.SlurmJobComponent) externaljobinfo.ExternalJobInfo {
	extInfo := externaljobinfo.ExternalJobInfo{}
	if group := externaljobinfo.NewGroupIdentity(root); group != nil {
		if group.Kind == "CompositePodGroup" {
			extInfo.CompositePodGroup = group
			extInfo.PodGroup = externaljobinfo.NewGroupIdentity(component.ObjectMeta)
		} else {
			extInfo.PodGroup = group
		}
	}
	for _, p := range component.Pods.Items {
		extInfo.Pods = append(extInfo.Pods, externaljobinfo.PodKey(&p))
	}
	return extInfo
}

func (r *realSlurmControl) buildJobDesc(root metav1.PartialObjectMetadata, jobComponent slurmjobir.SlurmJobComponent, update bool) (api.V0044JobDescMsg, error) {
	extInfo := ExternalJobInfoFor(root, jobComponent)
	constraints, err := gresCompatibilityConstraint(jobComponent.JobInfo.Constraints)
	if err != nil {
		return api.V0044JobDescMsg{}, err
	}
	excludedNodes := append(api.V0044CsvString{}, jobComponent.JobInfo.ExcNodes...)

	jobDesc := api.V0044JobDescMsg{
		Hold:                    jobComponent.JobInfo.Hold,
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
	// Set the account only at submission too. Without slurmdbd, Slurm's account
	// lookup on update finds no association and fails with a stale errno.
	if !update {
		jobDesc.Shared = sharedFromExclusiveAnnotation(&jobComponent)
	} else {
		jobDesc.Account = nil
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

	if jobDesc.Hold != nil || (update && jobComponent.JobInfo.Priority == nil) {
		jobDesc.Priority = nil
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
	resources, err := r.GetResourcesForNodes(ctx, pod, []string{nodeName})
	if err != nil {
		return nil, err
	}
	if node := resources[nodeName]; node != nil {
		return node, nil
	}
	return &NodeResources{}, nil
}

var resourceNodeReads = make(chan struct{}, 8)

// GetResourcesForNodes reads a component's resource layout once. Native gang
// binding retains the returned snapshot for all members in this attempt.
func (r *realSlurmControl) GetResourcesForNodes(ctx context.Context, pod *corev1.Pod, nodeNames []string) (map[string]*NodeResources, error) {
	out := make(map[string]*NodeResources, len(nodeNames))
	requested := make(map[string]bool, len(nodeNames))
	for _, node := range nodeNames {
		requested[node] = true
	}
	logger := klog.FromContext(ctx)

	nodes := &api.V0044NodeResourceLayoutListObject{}
	jobId := object.ObjectKey(pod.Labels[wellknown.LabelExternalJobId])
	if jobId == "" {
		return out, nil
	}

	err := r.Get(ctx, jobId, nodes, &client.GetOptions{SkipCache: true})
	if err != nil {
		logger.Error(err, "could not get node resource layout for pod", "pod", klog.KObj(pod))
		return nil, err
	}
	for _, n := range *nodes {
		if !requested[n.Node] {
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
		out[n.Node] = &nodeOut
	}
	// Fetch node metadata concurrently, with a process-wide bound so multiple
	// independently binding components cannot multiply remote request concurrency.
	var workers sync.WaitGroup
	failures := make(chan error, len(out))
	for name, resources := range out {
		workers.Go(func() {
			select {
			case resourceNodeReads <- struct{}{}:
				defer func() { <-resourceNodeReads }()
			case <-ctx.Done():
				failures <- ctx.Err()
				return
			}
			extra, err := r.getNodeExtra(ctx, name)
			if err != nil {
				failures <- err
				return
			}
			resources.NodeExtra = extra
		})
	}
	workers.Wait()
	close(failures)
	var failure error
	for err := range failures {
		failure = errors.Join(failure, err)
	}
	if failure != nil {
		return nil, failure
	}
	return out, nil
}

// Read Extra live: it maps GRES indexes to devices and changes when a node re-registers.
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
	nodes := slurmClient.GetInformer(api.ObjectTypeV0044Node)
	if err := nodes.AddIndex(nodePartitionIndex, nodePartitionKeys); err != nil {
		return nil, err
	}
	return &realSlurmControl{
		Client:    slurmClient,
		PodIndex:  index,
		mcsLabel:  mcsLabel,
		partition: partition,
		nodes:     nodes,
		// Fixed cap; make it configurable if pending-job counts outgrow it.
		lastUpdate: lru.New(10000),
	}, nil
}
