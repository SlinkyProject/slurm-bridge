# QuickStart Guide

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [QuickStart Guide](#quickstart-guide)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Pre-requisites](#pre-requisites)
  - [Installation](#installation)
    - [1. Install the required helm charts:](#1-install-the-required-helm-charts)
    - [2. Create a secret for `slurm-bridge`:](#2-create-a-secret-for-slurm-bridge)
    - [2. Download and configure `values.yaml` for the `slurm-bridge` helm chart](#2-download-and-configure-valuesyaml-for-the-slurm-bridge-helm-chart)
    - [3. Install the `slurm-bridge` Helm Chart:](#3-install-the-slurm-bridge-helm-chart)
  - [Running Your First Job](#running-your-first-job)

<!-- mdformat-toc end -->

## Overview

This quickstart guide will help you get the slurm-bridge running and configured
with your existing Slurm cluster.

This document assumes a basic understanding of
[Kubernetes architecture](https://kubernetes.io/docs/concepts/architecture/). It
is highly recommended that those who are unfamiliar with the core concepts of
Kubernetes review the documentation on
[Kubernetes](https://kubernetes.io/docs/concepts/overview/),
[pods](https://kubernetes.io/docs/concepts/workloads/pods/), and
[nodes](https://kubernetes.io/docs/concepts/architecture/nodes/) before getting
started.

## Pre-requisites

- A functional Slurm cluster with:
  - A set of hosts within the cluster that are running both a [kubelet] and
    [slurmd]
  - At least one partition consisting solely of nodes with the above
    configuration
  - Nodes configured with at least CPUs and RealMemory so that pods
    request can be matched with Slurm's Nodes.
  - MCS labels enabled:
    ```conf
    # slurm.conf
    ...
    MCSPlugin=mcs/label
    MCSParameters=ondemand,ondemandselect
    ```
- A functional Kubernetes cluster that includes the hosts running colocated
  [kubelet] and [slurmd]
- Matching NodeNames in Slurm and Kubernetes for all overlapping nodes
  - In the event that the colocated node's Slurm NodeName does not match the
    Kubernetes Node name, you should patch the Kubernetes node with a label to
    allow `slurm-bridge` to map the colocated Kubernetes and Slurm node.
    ```bash
    kubectl patch node $KUBERNETES_NODENAME -p "{\"metadata\":{\"labels\":{\"slinky.slurm.net/slurm-nodename\":\"$SLURM_NODENAME\"}}}"
    ```

## Installation

##### 1. Install the required helm charts:

```bash
helm repo update
helm install cert-manager jetstack/cert-manager \
	--namespace cert-manager --create-namespace --set crds.enabled=true
```

##### 2. Create a secret for `slurm-bridge`:

Create a secret for slurm-bridge to communicate with Slurm.

###### 2.1. When running Slurm in `slurm-operator`:

When running Slurm in `slurm-operator`:

```sh
kubectl apply -f - <<EOF
apiVersion: slinky.slurm.net/v1alpha1
kind: Token
metadata:
  name: slurm-bridge-token
  namespace: slinky
spec:
  jwtHs256KeyRef:
    name: slurm-auth-jwths256
    key: jwt_hs256.key
    namespace: slurm
  secretRef:
    name: slurm-bridge-token
    key: auth-token
  username: slurm
  refresh: true
  lifetime: 8760h
EOF
```

> [!NOTE]
> A long lifetime is used as `slurm-bridge` does not automatically restart when
> the secret is refreshed. This is a limitation that will be addressed in a
> subsequent release.

###### 2.1. When running Slurm on baremetal:

You need to generate a token for the target user and create a new secret with it.

```sh
export $(scontrol token username=slurm lifespan=infinite)
kubectl create namespace slurm-bridge
kubectl create secret generic slurm-bridge-token --namespace=slinky --from-literal="auth-token=$SLURM_JWT" --type=Opaque
```

Also make sure your slurmrestd exposes at least v0.0.44 API version.
Use the following command to check available API versions:

```sh
~$ slurmrestd -d list
Possible data_parser plugins:
data_parser/v0.0.44
data_parser/v0.0.42
data_parser/v0.0.43
data_parser/v0.0.41
~$ 
```

##### 2. Download and configure `values.yaml` for the `slurm-bridge` helm chart

The helm chart used by `slurm-bridge` has a number of parameters in
[values.yaml](https://github.com/SlinkyProject/slurm-bridge/blob/main/helm/slurm-bridge/values.yaml)
that can be modified to tweak various parameters of slurm-bridge. Most of these
values should work without modification.

Depending on your Slurm configuration, you may need to configure the following
variables:

- `schedulerConfig.partition` - this is the default partition with which
  `slurm-bridge` will associate jobs. This partition should only include nodes
  that have both [slurmd] and the [kubelet] running. The default value of this
  variable is `slurm-bridge`.
- `sharedConfig.slurmRestApi` - the URL used by `slurm-bridge` to interact with
  the Slurm REST API. Changing this value may be necessary if you run the REST
  API on a different URL or port. The default value of this variable is
  `http://slurm-restapi.slurm:6820`

##### 3. Install the `slurm-bridge` Helm Chart:

```bash
helm install slurm-bridge oci://ghcr.io/slinkyproject/charts/slurm-bridge \
  --namespace=slinky --create-namespace
```

> [!NOTE]
> `slurm-bridge` must be able to communicate with Slurm REST API. By default, it
> assumes a default Slurm chart installation and uses
> http://slurm-restapi.slurm:6820.

You can check if your cluster deployed successfully with:

```sh
kubectl --namespace=slinky get pods
```

Your output should be similar to:

```sh
NAME                                        READY   STATUS    RESTARTS      AGE
slurm-bridge-admission-85f89cf884-8c9jt     1/1     Running   0             1m0s
slurm-bridge-controllers-757f64b875-bsfnf   1/1     Running   0             1m0s
slurm-bridge-scheduler-5484467f55-wtspk     1/1     Running   0             1m0s
```

## Running Your First Job

`slurm-bridge` has specific scheduling support for [JobSet](#jobsets) and
[PodGroup](#podgroups) resources and their pods. If your workload requires or
benefits from co-scheduled pod launch (e.g. MPI, multi-node), consider
representing your workload as a [JobSet](#jobsets) or [PodGroup](#podgroups).

Now that `slurm-bridge` is configured, we can write a workload. `slurm-bridge`
schedules Kubernetes workloads using the Slurm scheduler by translating a
Kubernetes workload in the form of a [Jobs], [JobSets], [Pods], and [PodGroups]
into a representative Slurm job, which is used for scheduling purposes. Once a
workload is allocated resources, the Kubelet binds the Kubernetes workload to
the allocated resources and executes it. There are sample workload definitions
in the `slurm-bridge` repo
[here](https://github.com/SlinkyProject/slurm-bridge/tree/main/hack/examples).

Here's an example of a simple job, found in `hack/examples/single.yaml`:

```yaml
---
apiVersion: batch/v1
kind: Job
metadata:
  name: job-sleep-single
  namespace: slurm-bridge
  # slurm-bridge annotations on parent object
  annotations:
    slinky.slurm.net/job-name: job-sleep-single
    slinky.slurm.net/timelimit: "5"
    slinky.slurm.net/account: foo
spec:
  completions: 1
  parallelism: 1
  template:
    spec:
      containers:
        - name: sleep
          image: busybox:stable
          command: [sh, -c, sleep 30]
          resources:
            requests:
              cpu: '1'
              memory: 100Mi
            limits:
              cpu: '1'
              memory: 100Mi
      restartPolicy: Never
```

Let's run this job:

```bash
❯ kubectl apply -f hack/examples/job/single.yaml
job.batch/job-sleep-single created
```

At this point, Kubernetes has dispatched our job, it was scheduled by Slurm, and
executed to completion. Let's take a look at each place that our job shows up.

On the Slurm side, we can observe the placeholder job that was used to schedule
our workload.

First, look at the job STATUS in Kubernetes:

```bash
$ kubectl get jobs -n slurm-bridge
NAME                 STATUS     COMPLETIONS   DURATION   AGE
job-sleep-single     Complete   1/1           8s         8m
```

Next, describe the job. Under the `Events` section, note the name of the pod on
which the job executed. Describe that pod:

```bash
$ kubectl describe job -n slurm-bridge job-sleep-single
Name:             job-sleep-single
Namespace:        slurm-bridge
Selector:         batch.kubernetes.io/controller-uid=7cf47949-0099-4c1a-ab7e-d6e288283c82
Labels:           batch.kubernetes.io/controller-uid=7cf47949-0099-4c1a-ab7e-d6e288283c82
                  batch.kubernetes.io/job-name=job-sleep-single
                  controller-uid=7cf47949-0099-4c1a-ab7e-d6e288283c82
                  job-name=job-sleep-single
Annotations:      slinky.slurm.net/job-name: job-sleep-single
Parallelism:      1
Completions:      1
Completion Mode:  NonIndexed
Start Time:       Mon, 15 Sep 2025 11:38:48 -0600
Completed At:     Mon, 15 Sep 2025 11:38:58 -0600
Duration:         10s
Pods Statuses:    0 Active (0 Ready) / 1 Succeeded / 0 Failed
Pod Template:
  Labels:  batch.kubernetes.io/controller-uid=7cf47949-0099-4c1a-ab7e-d6e288283c82
           batch.kubernetes.io/job-name=job-sleep-single
           controller-uid=7cf47949-0099-4c1a-ab7e-d6e288283c82
           job-name=job-sleep-single
  Containers:
   sleep:
    Image:      busybox:stable
    Port:       <none>
    Host Port:  <none>
    Command:
      sh
      -c
      sleep 3
    Limits:
      cpu:     1
      memory:  100Mi
    Requests:
      cpu:        1
      memory:     100Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
Events:
  Type    Reason            Age   From            Message
  ----    ------            ----  ----            -------
  Normal  SuccessfulCreate  28m   job-controller  Created pod: job-sleep-single-w4dfl
  Normal  Completed         27m   job-controller  Job completed
```

Use the `kubectl get pod` command to get the labels for the pod in which the job
executed:

```bash
$ kubectl get pod -n slurm-bridge --show-labels
NAME                     READY   STATUS      RESTARTS   AGE   LABELS
job-sleep-single-w4dfl   0/1     Completed   0          31m   batch.kubernetes.io/controller-uid=7cf47949-0099-4c1a-ab7e-d6e288283c82,batch.kubernetes.io/job-name=job-sleep-single,controller-uid=7cf47949-0099-4c1a-ab7e-d6e288283c82,job-name=job-sleep-single,scheduler.slinky.slurm.net/slurm-jobid=1
```

The `scheduler.slinky.slurm.net/slurm-jobid` label tells us that the Slurm JobID
for our job was 1:

```bash
scheduler.slinky.slurm.net/slurm-jobid=1
```

```bash
slurm@slurm-controller-0:/tmp$ scontrol show job 1
JobId=1 JobName=job-sleep-single
   UserId=slurm(401) GroupId=slurm(401) MCS_label=kubernetes
   Priority=1 Nice=0 Account=(null) QOS=normal
   JobState=CANCELLED Reason=None Dependency=(null)
   Requeue=1 Restarts=0 BatchFlag=0 Reboot=0 ExitCode=0:0
   RunTime=00:00:08 TimeLimit=UNLIMITED TimeMin=N/A
   SubmitTime=2025-07-10T15:52:53 EligibleTime=2025-07-10T15:52:53
   AccrueTime=2025-07-10T15:52:53
   StartTime=2025-07-10T15:52:53 EndTime=2025-07-10T15:53:01 Deadline=N/A
   SuspendTime=None SecsPreSuspend=0 LastSchedEval=2025-07-10T15:52:53 Scheduler=Main
   Partition=slurm-bridge AllocNode:Sid=10.244.5.5:1
   ReqNodeList=(null) ExcNodeList=(null)
   NodeList=slurm-bridge-1
   BatchHost=slurm-bridge-1
   StepMgrEnabled=Yes
   NumNodes=1 NumCPUs=4 NumTasks=1 CPUs/Task=1 ReqB:S:C:T=0:0:*:*
   ReqTRES=cpu=1,mem=96046M,node=1,billing=1
   AllocTRES=cpu=4,mem=96046M,node=1,billing=4
   Socks/Node=* NtasksPerN:B:S:C=0:0:*:* CoreSpec=*
   MinCPUsNode=1 MinMemoryNode=0 MinTmpDiskNode=0
   Features=(null) DelayBoot=00:00:00
   OverSubscribe=NO Contiguous=0 Licenses=(null) LicensesAlloc=(null) Network=(null)
   Command=(null)
   WorkDir=/tmp
   AdminComment={"pods":["slurm-bridge/job-sleep-single-8wtc2"]}
   OOMKillStep=0
```

Note that the `Command` field is equal to `(null)`, and that the `JobState`
field is equal to `CANCELLED`. This is because this Slurm job is only a
placeholder - no work is actually done by the placeholder. Instead, the job is
cancelled upon allocation so that the Kubelet can bind the workload to the
selected node(s) for the duration of the job.

We can also look at this job using `kubectl`:

```bash
❯ kubectl describe job --namespace=slurm-bridge job-sleep-single
Name:             job-sleep-single
Namespace:        slurm-bridge
Selector:         batch.kubernetes.io/controller-uid=8a03f5f6-f0c0-4216-ac0b-8c9b70c92eec
Labels:           batch.kubernetes.io/controller-uid=8a03f5f6-f0c0-4216-ac0b-8c9b70c92eec
                  batch.kubernetes.io/job-name=job-sleep-single
                  controller-uid=8a03f5f6-f0c0-4216-ac0b-8c9b70c92eec
                  job-name=job-sleep-single
Annotations:      slinky.slurm.net/job-name: job-sleep-single
Parallelism:      1
Completions:      1
Completion Mode:  NonIndexed
Start Time:       Thu, 10 Jul 2025 09:52:53 -0600
Completed At:     Thu, 10 Jul 2025 09:53:02 -0600
Duration:         9s
Pods Statuses:    0 Active (0 Ready) / 1 Succeeded / 0 Failed
Pod Template:
  Labels:  batch.kubernetes.io/controller-uid=8a03f5f6-f0c0-4216-ac0b-8c9b70c92eec
           batch.kubernetes.io/job-name=job-sleep-single
           controller-uid=8a03f5f6-f0c0-4216-ac0b-8c9b70c92eec
           job-name=job-sleep-single
  Containers:
   sleep:
    Image:      busybox:stable
    Port:       <none>
    Host Port:  <none>
    Command:
      sh
      -c
      sleep 3
    Limits:
      cpu:     1
      memory:  100Mi
    Requests:
      cpu:        1
      memory:     100Mi
    Environment:  <none>
    Mounts:       <none>
  Volumes:        <none>
Events:
  Type    Reason            Age   From            Message
  ----    ------            ----  ----            -------
  Normal  SuccessfulCreate  14m   job-controller  Created pod: job-sleep-single-8wtc2
  Normal  Completed         14m   job-controller  Job completed
```

As Kubernetes is the context in which this job actually executed, this is
generally the more useful of the two outputs.

At this point, you should have a functional `slurm-bridge` cluster, and are
running jobs. Recommended next steps involve reviewing our documentation on
[workloads]

In case the job did not properly launched, try to bypass the admission controler and
check if the scheduler got the job. To do so, update the hack/examples/single.yaml file,
and add key `schedulerName` set to `slurm-bridge-scheduler` under spec:

```yaml
    spec:
      schedulerName: slurm-bridge-scheduler
```

If the scheduler got the job but fail to schedule (FailedScheduling),
check your Slurm cluster's slurmctld service logs for errors.

<!-- Links -->

[jobsets]: https://jobset.sigs.k8s.io/
[kubelet]: https://kubernetes.io/docs/reference/command-line-tools-reference/kubelet
[slurmd]: https://slurm.schedmd.com/slurmd.html
[workloads]: workload.md
