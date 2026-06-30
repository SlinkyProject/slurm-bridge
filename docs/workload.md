# Workloads

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Workloads](#workloads)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Using the `slurm-bridge` Scheduler](#using-the-slurm-bridge-scheduler)
  - [CPU DRA](#cpu-dra)
  - [Annotations](#annotations)
  - [PodGroup (1.36+)](#podgroup-136)
  - [JobSets](#jobsets)
  - [PodGroup coscheduling](#podgroup-coscheduling)
  - [LeaderWorkerSet](#leaderworkerset)

<!-- mdformat-toc end -->

## Overview

In Slurm, all workloads are represented by jobs. In `slurm-bridge`, however,
there are a number of forms that workloads can take. While workloads can still
be submitted as a Slurm job, `slurm-bridge` also enables users to submit
workloads through Kubernetes. Most workloads that can be submitted to
`slurm-bridge` from within Kubernetes are represented by an existing Kubernetes
batch workload primitive.

At this time, `slurm-bridge` has scheduling support for [Jobs],
[JobSets](#jobsets), [Pods], [PodGroup (1.36+)](#podgroup-136)
(`scheduling.k8s.io/v1alpha2`), [PodGroup coscheduling](#podgroup-coscheduling)
(scheduler-plugins), and [LeaderWorkerSets]. If your workload requires or
benefits from co-scheduled pod launch (e.g. MPI, multi-node), prefer
[PodGroup (1.36+)](#podgroup-136) on Kubernetes **1.36+** or
[PodGroup coscheduling](#podgroup-coscheduling) on older clusters.

## Using the `slurm-bridge` Scheduler

`slurm-bridge` uses an
[admission controller](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/)
to control which resources are scheduled using the `slurm-bridge-scheduler`. The
`slurm-bridge-scheduler` is designed as a non-primary scheduler and is not
intended to replace the default
[kube-scheduler](https://kubernetes.io/docs/concepts/architecture/#kube-scheduler).
The `slurm-bridge` admission controller only schedules pods that request
`slurm-bridge` as their scheduler or are in a configured namespace. By default,
the `slurm-bridge` admission controller is configured to automatically use
`slurm-bridge` as the scheduler for all pods in the configured namespaces.

Alternatively, a pod can specify `Pod.Spec.schedulerName=slurm-bridge-scheduler`
from any namespace to indicate that it should be scheduler using the
`slurm-bridge-scheduler`.

Please review [`slurm-bridge` admission controller](./admission.md) to learn
more.

## CPU DRA

Native `cpu` requests do not activate the CPU DRA driver. To request CPUs from
the `dra.cpu` DeviceClass, specify its extended resource explicitly:

```yaml
resources:
  requests:
    deviceclass.resource.kubernetes.io/dra.cpu: "2"
  limits:
    deviceclass.resource.kubernetes.io/dra.cpu: "2"
```

The extended resource quantity is used as the Slurm CPU count. A Pod that
requests this resource cannot also specify native `cpu` requests or limits.

CPU DRA constrains the container to Slurm's allocated CPU set. The CPU driver
also removes DRA-allocated CPUs from the shared CPU sets of running native
containers. Native CPU requests still reserve capacity in Slurm, but native
containers share all CPUs not claimed through DRA; Slurm's native CPU IDs do not
define their container CPU sets.

## Annotations

Users can better inform or influence `slurm-bridge` how to represent their
Kubernetes workload within Slurm by adding
[annotations](../internal/wellknown/annotations.go) on the parent Object.

Example "pause" bare pod to illustrate annotations:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pause
  # `slurm-bridge` annotations on parent object
  annotations:
    slurmjob.slinky.slurm.net/timelimit: "5"
    slurmjob.slinky.slurm.net/account: foo
spec:
  schedulerName: slurm-bridge-scheduler
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.6
      resources:
        limits:
          cpu: "1"
          memory: 100Mi
```

Example "sleep" Job to illustrate annotations:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: sleep
  # `slurm-bridge` annotations on parent object
  annotations:
    slurmjob.slinky.slurm.net/timelimit: "5"
    slurmjob.slinky.slurm.net/account: foo
spec:
  template:
    spec:
      schedulerName: slurm-bridge-scheduler
      restartPolicy: Never
      containers:
        - name: sleep
          image: busybox:stable
          command: [sh, -c, sleep 3]
          resources:
            limits:
              cpu: "1"
              memory: 100Mi
```

## PodGroup (1.36+)

PodGroup is a built-in Kubernetes API introduced in **1.36**. This section
applies to clusters running **1.36+** with the **`GenericWorkload`** feature
gate and **`scheduling.k8s.io/v1alpha2`** API enabled (see
[`hack/kind.yaml`](../hack/kind.yaml) and `make demo-examples`). After
slurm-bridge assigns nodes to the gang, PodGroup `STATUS` becomes **Scheduled**
(`PodGroupScheduled=True`); it is not tied to Job completion.

A [**Workload**][workload-api] defines immutable **`podGroupTemplates`** (gang
or basic scheduling). Workload controllers create runtime **`PodGroup`** objects
from those templates. Pods opt in with **`spec.schedulingGroup.podGroupName`**
pointing at their **`PodGroup`**. `slurm-bridge` reads the PodGroup, groups pods
by scheduling group, and applies the same external-job flow as other
co-scheduled workload types.

Example manifests (see also
[`hack/examples/workload/`](../hack/examples/workload/)):

```yaml
apiVersion: scheduling.k8s.io/v1alpha2
kind: Workload
metadata:
  name: training-workload
  annotations:
    slurmjob.slinky.slurm.net/job-name: training-job
    slurmjob.slinky.slurm.net/timelimit: "5"
spec:
  controllerRef:
    apiGroup: batch
    kind: Job
    name: training-job
  podGroupTemplates:
    - name: workers
      schedulingPolicy:
        gang:
          minCount: 2
---
apiVersion: scheduling.k8s.io/v1alpha2
kind: PodGroup
metadata:
  name: training-job-workers
spec:
  podGroupTemplateRef:
    workload:
      workloadName: training-workload
      podGroupTemplateName: workers
  schedulingPolicy:
    gang:
      minCount: 2
---
apiVersion: batch/v1
kind: Job
metadata:
  name: training-job
spec:
  template:
    spec:
      schedulerName: slurm-bridge-scheduler
      schedulingGroup:
        podGroupName: training-job-workers
```

Ref: [Workload API][workload-api]

To override Slurm submission parameters, add optional
`slurmjob.slinky.slurm.net/*` annotations on the **Workload**, owning **Job**,
or runtime **PodGroup**. On conflict, **Workload** > **Job** > **PodGroup**.
Without them, the Slurm job name defaults to the **PodGroup object name** (not
the Workload name) and the partition defaults to the scheduler configuration.
See [Annotations](#annotations) for the full key list.

If any layer sets `slurmjob.slinky.slurm.net/job-name`, later layers in that
merge order overwrite earlier ones. In the example above, the PodGroup is named
`training-job-workers` but the Workload sets
`slurmjob.slinky.slurm.net/job-name: training-job`, so Slurm receives
**`training-job`**. A PodGroup cannot override a `job-name` set on its Workload
or owning Job; put per-gang `job-name` on the **PodGroup** or **Job** instead.

A Workload may define several `podGroupTemplates`, each producing a runtime
PodGroup. Workload-level identifiers such as `job-name` then apply to **every**
PodGroup under that Workload. Each gang still submits a separate Slurm external
job (distinct job ID on the pods), but all share the same Slurm job **name** in
`squeue`. Use the Workload for **shared** parameters (partition, account, QOS,
time limit) and **PodGroup** or **Job** for per-gang identifiers.

## JobSets

This section assumes [JobSets] is installed.

JobSet pods are scheduled on a per-pod basis. The JobSet controller is
responsible for managing the JobSet status and other Pod interactions once
marked as completed.

## PodGroup coscheduling

This is **not** the same API as [PodGroup (1.36+)](#podgroup-136) above. It uses
the **scheduler-plugins** CRD `scheduling.x-k8s.io/v1alpha1` and requires
installing on clusters **before 1.36** (or where the built-in PodGroup API is
unavailable) the [PodGroup coscheduling CRD][podgroups-crd] plus the out-of-tree
CoScheduling controller:

```sh
helm install --repo https://scheduler-plugins.sigs.k8s.io scheduler-plugins scheduler-plugins \
  --namespace scheduler-plugins --create-namespace \
  --set 'plugins.enabled={CoScheduling}' --set 'scheduler.replicaCount=0'
```

Pods join the group via the label `scheduling.x-k8s.io/pod-group` (see
[`hack/examples/podgroup-coscheduling/`](../hack/examples/podgroup-coscheduling/)).
Gang size is `spec.minMember` on the PodGroup object.

|                 | PodGroup (1.36+)                      | PodGroup coscheduling                 |
| --------------- | ------------------------------------- | ------------------------------------- |
| API group       | `scheduling.k8s.io/v1alpha2`          | `scheduling.x-k8s.io/v1alpha1`        |
| Install         | Feature gate + runtime config         | CRD + helm chart                      |
| Pod association | `spec.schedulingGroup.podGroupName`   | Label `scheduling.x-k8s.io/pod-group` |
| Gang field      | `spec.schedulingPolicy.gang.minCount` | `spec.minMember`                      |

Both paths are supported by `slurm-bridge` independently.

## LeaderWorkerSet

This section assumes [LeaderWorkerSet][leaderworkersets] is installed.

LeaderWorkerSet groups will be co-scheduled so pods of each group will be
guaranteed to launch together.

> [!NOTE]
> Topology-aware placement is not supported yet, so some features of
> LeaderWorkerSet may not behave as expected.

<!-- Links -->

[jobs]: https://kubernetes.io/docs/concepts/workloads/controllers/job/
[jobsets]: https://jobset.sigs.k8s.io/
[leaderworkersets]: https://lws.sigs.k8s.io/
[podgroups-crd]: https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/config/crd/bases/scheduling.x-k8s.io_podgroups.yaml
[pods]: https://kubernetes.io/docs/concepts/workloads/pods/
[workload-api]: https://kubernetes.io/docs/concepts/workloads/workload-api/
