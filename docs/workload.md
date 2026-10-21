# Workloads

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Workloads](#workloads)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Using the `slurm-bridge` Scheduler](#using-the-slurm-bridge-scheduler)
  - [Device resources](#device-resources)
    - [Supported DRA DeviceClasses](#supported-dra-deviceclasses)
    - [Legacy GPU device plugins](#legacy-gpu-device-plugins)
  - [CPU DRA](#cpu-dra)
  - [Annotations](#annotations)
    - [Resolution rules](#resolution-rules)
    - [Supported Slurm job annotations](#supported-slurm-job-annotations)
    - [Scheduler-managed Pod metadata](#scheduler-managed-pod-metadata)
  - [Pod grouping](#pod-grouping)
    - [Other controller owners](#other-controller-owners)
  - [Built-in PodGroup](#built-in-podgroup)
    - [Upgrading from Kubernetes 1.36 to 1.37](#upgrading-from-kubernetes-136-to-137)
  - [JobSets](#jobsets)
  - [PodGroup coscheduling](#podgroup-coscheduling)
    - [Migrating from v1alpha1 PodGroups](#migrating-from-v1alpha1-podgroups)
  - [LeaderWorkerSet](#leaderworkerset)
  - [Scaling and failure behavior](#scaling-and-failure-behavior)
    - [CPU and memory changes](#cpu-and-memory-changes)
    - [Changing the number of Pods](#changing-the-number-of-pods)
    - [Scale-down and resource release](#scale-down-and-resource-release)
    - [Failures and recovery](#failures-and-recovery)

<!-- mdformat-toc end -->

## Overview

In Slurm, all workloads are represented by jobs. In `slurm-bridge`, however,
there are a number of forms that workloads can take. While workloads can still
be submitted as a Slurm job, `slurm-bridge` also enables users to submit
workloads through Kubernetes. Most workloads that can be submitted to
`slurm-bridge` from within Kubernetes are represented by an existing Kubernetes
batch workload primitive.

At this time, `slurm-bridge` has scheduling support for [Jobs],
[JobSets](#jobsets), [Pods], [built-in PodGroup](#built-in-podgroup)
(`scheduling.k8s.io/v1alpha2` on Kubernetes **1.36**, or
`scheduling.k8s.io/v1beta1` on **1.37**),
[PodGroup coscheduling](#podgroup-coscheduling) (scheduler-plugins, deprecated),
and [LeaderWorkerSets]. If your workload requires or benefits from co-scheduled
pod launch (e.g. MPI, multi-node), use [built-in PodGroup](#built-in-podgroup)
on Kubernetes **1.36 or 1.37** when its API is enabled. The deprecated
[PodGroup coscheduling](#podgroup-coscheduling) integration remains available
for compatibility when the built-in API is unavailable.

## Using the `slurm-bridge` Scheduler

`slurm-bridge` uses an
[admission controller](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/)
to control which resources are scheduled using the `slurm-bridge-scheduler`. The
`slurm-bridge-scheduler` is designed as a non-primary scheduler and is not
intended to replace the default
[kube-scheduler](https://kubernetes.io/docs/concepts/architecture/#kube-scheduler).
The `slurm-bridge-scheduler` only schedules pods that request `slurm-bridge` as
their scheduler or are in a configured namespace. By default, the `slurm-bridge`
admission controller is configured to automatically use `slurm-bridge` as the
scheduler for all pods in the configured namespaces.

Alternatively, a pod can specify `Pod.Spec.schedulerName=slurm-bridge-scheduler`
from any namespace to indicate that it should be scheduled using the
`slurm-bridge-scheduler`.

Please review [`slurm-bridge` admission controller](./admission.md) to learn
more.

## Device resources

### Supported DRA DeviceClasses

`slurm-bridge` supports the following DRA DeviceClass extended resources out of
the box:

| DeviceClass      | Extended resource                                   | Device type |
| ---------------- | --------------------------------------------------- | ----------- |
| `dra.cpu`        | `deviceclass.resource.kubernetes.io/dra.cpu`        | CPU         |
| `gpu.nvidia.com` | `deviceclass.resource.kubernetes.io/gpu.nvidia.com` | NVIDIA GPU  |

For these resources, `slurm-bridge` translates the Slurm allocation into a DRA
ResourceClaim and records the allocated devices for the Pod. Additional indexed
DeviceClasses are supported when they resolve to a configured device profile.
Other DeviceClass extended resources are unsupported. Validation covers requests
and limits in both init containers and regular containers.

The bridge keeps `DRADeviceTaintRules` disabled by default because clusters
older than Kubernetes 1.37 do not serve `resource.k8s.io/v1` DeviceTaintRules.
Slurm chooses the devices, so Kubernetes device taints do not stop Slurm from
allocating a tainted device.

Indexed DRA devices are mapped to Slurm GRES through `deviceProfiles` in the
shared Slurm Bridge configuration. The built-in profiles cover CPU, NVIDIA GPU,
and PCI-backed DRANET devices. The example GPU driver requires explicit
configuration. Operators can replace or extend the list through
`sharedConfig.deviceProfiles`:

```yaml
sharedConfig:
  deviceProfiles:
    - name: custom-accelerator
      driver: accelerator.example.com
      selector: device.driver == 'accelerator.example.com'
      backend:
        type: indexed-gres
        gresName: accelerator
```

The DeviceClass must have exactly one CEL selector and it must exactly match a
configured profile selector. Profile names become Slurm GRES types: they must
start and end with an alphanumeric character, may contain only alphanumeric
characters, `.`, `_`, and `-`, and must remain stable while allocations using
them exist. `driver` must be a valid lowercase Kubernetes DRA driver name, and
`gresName` must be a DNS-1123 label of at most 60 characters because it is also
used as the ResourceClaim request name. Every configured `backend.gresName` must
also be listed in Slurm's `GresTypes`; for example, profiles using `gpu` and
`nic` require `GresTypes=gpu,nic`. Slurm omits a GRES from its node inventory
when its type is not listed. The bundled external and hybrid Kind configurations
include `GresTypes=gpu,nic` by default. Selectors use the same length and
estimated-cost limits as Kubernetes DeviceClass CEL selectors. Profiles for the
same driver must be mutually exclusive. If a device matches more than one
profile, the node controller leaves its Slurm GRES inventory unchanged and emits
an `OverlappingDRADeviceProfiles` Warning event on the Kubernetes Node.

The built-in NVIDIA profile uses `gpu.nvidia.com` as its name and maps to
`gpu:gpu.nvidia.com` in Slurm. The explicitly configured example GPU profile
uses `gpu.example.com` and maps to `gpu:gpu.example.com`. These preserve the
driver-named GRES types used before DeviceProfiles. Registered profiles require
the applied device-index mapping in the Slurm node's `Extra` field before
allocations can bind.

ResourceSlices must explicitly name a single node with a nonempty
`spec.nodeName`. `spec.nodeSelector`, `spec.allNodes`, and
`spec.perDeviceNodeSelection` are unsupported. All slices in the latest
generation of a driver/pool must name the same node and agree on
`resourceSliceCount`. That generation must be complete before the bridge uses
its devices. Multiple slices per pool are supported, including CPU driver pools;
an incomplete new generation does not fall back to an older generation.

The default `dranet-rdma` profile maps PCI-backed devices with DRANET's `rdma`
attribute set to the Slurm `nic` GRES. It includes InfiniBand, RoCE, and iWARP
devices; it does not imply an InfiniBand link layer.

The [Kind e2e device profiles](../hack/e2e-device-profiles.yaml) explicitly add
the example GPU driver and a fixture-only `dranet0` profile alongside the
built-in profiles. The `dranet0` profile selects the dummy network interface
created by `hack/kind.sh --dranet` by driver and interface name:

```yaml
sharedConfig:
  deviceProfiles:
    - name: dranet0
      driver: dra.net
      selector: >-
        device.driver == 'dra.net' && has(device.attributes['dra.net'].ifName) &&
        device.attributes['dra.net'].ifName == 'dranet0'
      backend:
        type: indexed-gres
        gresName: nic
```

### Legacy GPU device plugins

The following non-DRA extended resources are also supported:

| Extended resource | Device plugin |
| ----------------- | ------------- |
| `nvidia.com/gpu`  | NVIDIA GPU    |
| `amd.com/gpu`     | AMD GPU       |

These resources use the generic device-plugin path. Their quantities are
translated to a generic Slurm `gres/gpu=<count>` request. Slurm selects and
reserves the node and GPU count, while kubelet and the device plugin choose the
physical devices. No DRA ResourceClaim is created, so Slurm and Kubernetes do
not coordinate exact GPU identities on this path.

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

Set `slurmjob.slinky.slurm.net/exclusive: "false"` on the workload's
[annotation source](#pod-grouping). Exclusive allocation is the default and is
unsupported for CPU DRA because the driver needs unallocated CPUs for shared
containers. Non-exclusive claims can also exhaust that pool; the driver rejects
allocations that would leave shared containers without CPUs.

CPU DRA constrains the container to Slurm's allocated CPU set. The CPU driver
also removes DRA-allocated CPUs from the shared CPU sets of running native
containers. Native CPU requests still reserve capacity in Slurm, but native
containers share all CPUs not claimed through DRA; Slurm's native CPU IDs do not
define their container CPU sets.

CPU DRA requires Kubernetes 1.36 or newer with `DRAExtendedResource` and
`DRAConsumableCapacity` enabled, plus containerd with CDI and NRI enabled. Use
the [pinned CPU driver](../hack/dra-driver-cpu/build.sh) with
`cpuDeviceMode: grouped`, `groupBy: socket`, and `allocator: external`. After
reserved CPUs are excluded, sockets must have equal core counts and cores must
have equal thread counts. Slurm must be the sole allocator for these devices;
kubelet's CPU Manager must not assign the same CPUs independently.

To migrate from individual CPU devices, drain Slurm nodes and wait for all
claims to be unprepared before replacing the driver and bridge. Use the same
procedure before changing CPU reservations or topology. Resume work after Slurm
registration matches the new inventory. Hybrid nodes also require matching
slurmd geometry; geometry checks do not verify Linux CPU ordering on physical
hardware.

## Annotations

Users can influence how `slurm-bridge` represents their Kubernetes workload in
Slurm by adding `slurmjob.slinky.slurm.net/*` annotations to the annotation
source identified in [Pod grouping](#pod-grouping). These annotations configure
the Slurm external job; they are separate from the
[scheduler-managed metadata](#scheduler-managed-pod-metadata).

Example "pause" bare pod to illustrate annotations:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pause
  # Slurm job annotations on this Pod
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
  # Slurm job annotations on the Job, not spec.template.metadata
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

### Resolution rules

Slurm parameters are resolved from lowest to highest precedence: **scheduler or
Slurm defaults -> values derived from the workload and Pods -> Slurm job
annotations**. For built-in PodGroups, annotation precedence is **PodGroup ->
selected controller (Job or JobSet) -> Workload**.

Keys that do not conflict are combined.

### Supported Slurm job annotations

| Purpose                 | Annotation suffixes                                                                   |
| ----------------------- | ------------------------------------------------------------------------------------- |
| Identity and accounting | `account`, `group-id`, `user-id`, `wckey`                                             |
| Scheduling policy       | `constraints`, `exclusive`, `licenses`, `partition`, `priority`, `qos`, `reservation` |
| Resources               | `cpu-per-task`, `gres`, `max-nodes`, `mem-per-node`, `min-nodes`                      |
| Naming and duration     | `job-name`, `timelimit`                                                               |

Prefix every suffix with `slurmjob.slinky.slurm.net/`; for example,
`slurmjob.slinky.slurm.net/account`. Node counts and priority are base-10
integers. CPU and memory accept Kubernetes quantities. `timelimit` accepts a
duration with a unit suffix (`s`, `m`, `h`, `d`, `w`), one of Slurm's `--time`
formats (`MM:SS`, `HH:MM:SS`, `D-HH`, `D-HH:MM`, `D-HH:MM:SS`), or a bare
integer, which is read as minutes; sub-minute values are rounded up to one
minute because Slurm's time limit has minute granularity. Exclusive placement is
the default. `exclusive: "false"` always requests MCS-category sharing; the
resulting external job uses Slurm's `Shared=mcs` mode and requires a configured
`schedulerConfig.mcsLabel`. Because Slurm's `mcs/label` plugin does not
authorize label use, production clusters must also reserve that label from
native users as described in
[Production label authorization](config.md#production-label-authorization).

Every bridge job requires the `slurm_bridge_gres_compatible` node feature, so
the scheduler combines it with the `constraints` value using Slurm's AND
operator. Slurm's constraint grammar does not allow nested parentheses, allows
one set of matching-OR brackets, and only accepts a feature count outside
brackets when it is the sole feature. Within those rules any expression is
supported, including `a&b`, `a|b`, `a&(b|c)`, `[rack1|rack2]` and
`[a100*2&h100*1]`. Admission rejects the few expressions that cannot be combined
with an additional feature: a bare count such as `rack1*2`, and an OR outside
parentheses mixed with parentheses or brackets such as `(a&b)|(c&d)`. Rewrite
these with a single level of grouping, for example `[a&b|c&d]`.

Annotations can update a Slurm job while it is pending. Slurm validates each
change. If it rejects an update, the previous Slurm job value remains in effect.
The `account` and `exclusive` annotations apply only at submission, so later
edits to them do not change the Slurm job. Once Slurm allocates the job, treat
its annotations and Pod membership as fixed.

### Scheduler-managed Pod metadata

Slurm-bridge writes the following metadata to Pods as it creates and allocates
the external job:

| Metadata                                 | Type       | Meaning                                                                                                |
| ---------------------------------------- | ---------- | ------------------------------------------------------------------------------------------------------ |
| `scheduler.slinky.slurm.net/slurm-jobid` | Label      | Slurm job ID associated with the Pod. All Pods represented by one external job receive the same value. |
| `slinky.slurm.net/slurm-node`            | Annotation | Kubernetes node selected from the Slurm allocation for this Pod.                                       |

These keys are owned by slurm-bridge. Users cannot set them when creating a
managed Pod or change them after that Pod is running.

## Pod grouping

Slurm-bridge turns each group of Kubernetes Pods into one Slurm external job:

| Workload                  | Pods in one external job                                 | Where to put annotations                      |
| ------------------------- | -------------------------------------------------------- | --------------------------------------------- |
| Pod                       | That Pod                                                 | Pod                                           |
| Job or JobSet             | One Pod                                                  | Job or JobSet                                 |
| Built-in PodGroup         | Pods with the same `spec.schedulingGroup.podGroupName`   | PodGroup, selected Job or JobSet, or Workload |
| PodGroup coscheduling     | Pods with the same `scheduling.x-k8s.io/pod-group` label | PodGroup                                      |
| LeaderWorkerSet           | One LeaderWorkerSet group                                | LeaderWorkerSet                               |
| Other readable controller | One Pod                                                  | Highest readable controller                   |

Slurm-bridge first follows controller owner references toward the root object.
It selects the first applicable grouping mechanism in this order: **built-in
PodGroup -> PodGroup coscheduling -> highest recognized workload type in the
owner chain -> the Pod itself**. When no recognized workload exists, the highest
readable controller remains the annotation source for the per-Pod external job.

For Jobs, JobSets, and LeaderWorkerSets, annotations belong on the top-level
workload object, not its Pod template or generated child objects.

For grouped workloads:

- **JobSet:** JobSet annotations apply to every per-Pod external job.
  Annotations on generated Jobs or Pods are ignored.
- **LeaderWorkerSet:** LeaderWorkerSet annotations apply to every group external
  job. Pod annotations are ignored.
- **Built-in PodGroup:** annotations are merged from the PodGroup, one selected
  controller (Job or JobSet), and Workload. If a Job belongs to a JobSet, the
  JobSet is selected and the intermediate Job's annotations are ignored. Pod
  annotations are also ignored.
- **PodGroup coscheduling:** only PodGroup annotations apply to the group.
  Owning Job and Pod annotations are ignored.

The built-in Workload API is therefore different from the other grouped
workloads: it merges scoped annotation sources instead of reading one top-level
source.

### Other controller owners

The Pod's direct controller owner must exist and be readable by the slurm-bridge
scheduler. While following the owner chain, slurm-bridge remembers the highest
recognized workload type. Higher readable controllers do not replace that
workload as the scheduling root. If no recognized workload is found,
slurm-bridge schedules each Pod as a separate external job and reads annotations
from the highest readable controller. Annotations on lower controllers and the
Pod are ignored.

If RBAC forbids access to a higher, unsupported controller, traversal stops and
slurm-bridge uses the highest recognized workload already found, or otherwise
the highest readable controller.

Slurm-bridge does not fall back when access to a supported workload type is
forbidden; that indicates missing scheduler RBAC. It also does not fall back
when an owner object is missing, its API kind is not served, or the API request
fails for another reason. These conditions indicate a broken owner chain or a
potentially transient cluster error, so scheduling fails instead.

The default scheduler RBAC can read ReplicaSets and StatefulSets, but not
Deployments, DaemonSets, or arbitrary custom controllers. For example, owner
resolution for `Deployment -> ReplicaSet -> Pod` stops at the ReplicaSet if the
scheduler cannot read the Deployment, and the Pod is still scheduled. If the
ReplicaSet itself cannot be retrieved, scheduling fails because no controller in
the chain was successfully resolved.

## Built-in PodGroup

`slurm-bridge` supports built-in Workload and PodGroup APIs on Kubernetes **1.36
and 1.37**. Enable the **`GenericWorkload`** feature gate and the API version
for your cluster:

| Kubernetes | Workload and PodGroup `apiVersion` | Kind configuration                              |
| ---------- | ---------------------------------- | ----------------------------------------------- |
| 1.36       | `scheduling.k8s.io/v1alpha2`       | [`hack/kind-1.36.yaml`](../hack/kind-1.36.yaml) |
| 1.37       | `scheduling.k8s.io/v1beta1`        | [`hack/kind.yaml`](../hack/kind.yaml)           |

The bridge's **`SlurmBridgeGenericWorkload`** feature gate is enabled by
default. At startup, the scheduler requires both Workload and PodGroup resources
in a supported API version, preferring `v1beta1` when both versions are
available. Missing APIs, incomplete discovery responses, and discovery errors
fail startup with an error identifying the required feature. The scheduler does
not silently disable Workload support.

This bridge gate is separate from Kubernetes' `GenericWorkload` gate. Enable
`GenericWorkload` on the cluster components as shown in the Kind configurations;
`SlurmBridgeGenericWorkload` controls the bridge's compatibility implementation
without enabling the embedded scheduler's upstream gang-scheduling path.

When the embedded scheduler's `GenericWorkload` gate is enabled, its shared
PodGroup client and informers use the discovered native API version. Alpha
objects and status conditions are converted to the scheduler's beta types;
status writes are converted back to the served version. Other Kubernetes
resources retain their original clients. This compatibility wiring does not
enable native gang scheduling by default.

Clusters without these APIs, including Kubernetes **1.35**, must explicitly opt
out with `--feature-gates=SlurmBridgeGenericWorkload=false`. For Helm
deployments, set `scheduler.featureGates.SlurmBridgeGenericWorkload=false`. The
scheduler then skips Workload discovery and registration. Pods that reference a
built-in PodGroup through `spec.schedulingGroup.podGroupName` are rejected with
a clear scheduling failure before Slurm operations. Ordinary Pods, Jobs,
JobSets, LeaderWorkerSets, and scheduler-plugins PodGroups retain their existing
behavior when they do not reference a built-in PodGroup.

For local Kubernetes 1.35 testing, use the `kubernetes-1-35` Skaffold profile.
The CI matrix's `KUBERNETES_VERSION=v1.35.x` environment setting activates this
profile automatically. Other test versions retain the default requirement.

After slurm-bridge assigns nodes to the gang, it sets `PodGroupScheduled=True`
on 1.36 or `PodGroupInitiallyScheduled=True` on 1.37. These conditions record
gang admission, independently of Job completion.

A [**Workload**][workload-api] defines **`podGroupTemplates`** (gang or basic
scheduling). Workload controllers create runtime **`PodGroup`** objects from
those templates. Pods opt in with **`spec.schedulingGroup.podGroupName`**
pointing at their **`PodGroup`**. For a **Gang** policy, `slurm-bridge` groups
pods by scheduling group and applies the same external-job flow as other
co-scheduled workload types, including the `minCount` check.

For a **Basic** policy, pods follow their normal owner-based scheduling path.
Jobs and standalone Pods receive independent Slurm allocations, while
LeaderWorkerSets retain their existing group scheduling. PodGroup and Workload
annotations still apply with the precedence described below. This also covers
the Basic PodGroups that Kubernetes 1.37 creates automatically for ordinary Jobs
when `WorkloadWithJob` is enabled: completed or failed sibling Pods do not
increase the allocation requested for the next Job Pod.

Example manifest excerpts for Kubernetes **1.37**:

```yaml
apiVersion: scheduling.k8s.io/v1beta1
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
apiVersion: scheduling.k8s.io/v1beta1
kind: PodGroup
metadata:
  name: training-job-workers
spec:
  workloadRef:
    workloadName: training-workload
    templateName: workers
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

For Kubernetes **1.36**, set `apiVersion: scheduling.k8s.io/v1alpha2` on both
the Workload and PodGroup, and replace the PodGroup's `spec.workloadRef` with
`spec.podGroupTemplateRef`:

```yaml
spec:
  podGroupTemplateRef:
    workload:
      workloadName: training-workload
      podGroupTemplateName: workers
```

Keep `spec.schedulingPolicy` and the Job's
`spec.template.spec.schedulingGroup.podGroupName` unchanged. The complete
manifests in [`hack/examples/workload/`](../hack/examples/workload/) use this
1.36 form.

Ref: [Workload API][workload-api]

To override Slurm submission parameters, add optional
`slurmjob.slinky.slurm.net/*` annotations on the **Workload**, selected
controller (**Job** or **JobSet**), or runtime **PodGroup**. On conflict,
**Workload** > **selected controller** > **PodGroup**. Without them, the Slurm
job name for a Gang policy defaults to the **PodGroup object name** (not the
Workload name); Basic policies retain the normal workload's naming behavior. The
partition defaults to the scheduler configuration. See
[Annotations](#annotations) for the full key list.

If multiple layers set `slurmjob.slinky.slurm.net/job-name`, annotations are
applied **PodGroup -> selected controller -> Workload**, so the Workload wins.
In the example above, the PodGroup is named `training-job-workers` but the
Workload sets `slurmjob.slinky.slurm.net/job-name: training-job`, so Slurm
receives **`training-job`**. A PodGroup cannot override a `job-name` set on its
Workload or selected controller. For per-gang names, omit `job-name` from
broader sources and set it on each **PodGroup** instead.

A Workload may define several `podGroupTemplates`, each producing a runtime
PodGroup. Workload-level identifiers such as `job-name` then apply to **every**
PodGroup under that Workload. Each gang still submits a separate Slurm external
job (distinct job ID on the pods), but all share the same Slurm job **name** in
`squeue`. Use the Workload for **shared** parameters (partition, account, QOS,
time limit) and **PodGroup** for per-gang identifiers.

### Upgrading from Kubernetes 1.36 to 1.37

Kubernetes 1.37 does not serve `scheduling.k8s.io/v1alpha2`, so Workloads and
PodGroups created on 1.36 are not available after the upgrade. A Pod's
`spec.schedulingGroup.podGroupName` cannot change. Running Pods keep running
with their Slurm jobs. Pending Pods wait until a PodGroup with that name exists
again.

1. Before the upgrade, save the objects you want to keep:

   ```sh
   kubectl get workloads.scheduling.k8s.io,podgroups.scheduling.k8s.io \
     --all-namespaces --output yaml > workloads-v1alpha2.yaml
   ```

1. Upgrade the cluster. Enable `scheduling.k8s.io/v1beta1` and the
   `GenericWorkload` gate, as in [`hack/kind.yaml`](../hack/kind.yaml).

1. Restart the scheduler so it discovers `v1beta1`:

   ```sh
   kubectl --namespace slurm rollout restart deployment/slurm-bridge-scheduler
   ```

1. Convert the saved objects and apply them with the same names and namespaces.
   Pending Pods then join their groups.

   - Set `apiVersion: scheduling.k8s.io/v1beta1`.
   - In PodGroups, replace
     `spec.podGroupTemplateRef.workload.{workloadName,podGroupTemplateName}`
     with `spec.workloadRef.{workloadName,templateName}`.
   - In PodGroups and in each Workload `spec.podGroupTemplates` entry, replace
     `disruptionMode: Pod` with `disruptionMode: {single: {}}` and
     `disruptionMode: PodGroup` with `disruptionMode: {all: {}}`.
   - Remove `status`, `metadata.resourceVersion`, and `metadata.uid`.

## JobSets

This section assumes [JobSets] is installed.

JobSet pods are scheduled on a per-pod basis unless a Gang PodGroup groups them
into a shared Slurm allocation. The Job and JobSet controllers manage retries,
failure policies, and completion; slurm-bridge manages the corresponding Slurm
jobs.

JobSet API validation also limits scaling. In JobSet v0.13.0, changing
`spec.replicatedJobs[].replicas` or the Pod resource template is rejected and
requires recreating the JobSet. The upstream `ElasticJobSet` feature gate
(disabled by default) permits changing `parallelism` and `completions` together
for non-terminal Indexed Jobs, with equal values. Without gang grouping, each
additional Pod gets an independent Slurm job. With gang grouping, the allocation
limitations below still apply, even if the JobSet API accepts the update.

See [Scaling and failure behavior](#scaling-and-failure-behavior) before
changing an existing workload.

## PodGroup coscheduling

> [!WARNING]
> Support for the **scheduler-plugins** PodGroup API
> `scheduling.x-k8s.io/v1alpha1` is deprecated in `slurm-bridge` and will be
> removed in an upcoming release. Use the
> [built-in PodGroup](#built-in-podgroup) API for new workloads. Existing legacy
> workloads remain supported, but the [admission webhook](admission.md) warns on
> Pod creation and update when the `scheduling.x-k8s.io/pod-group` label is
> non-empty. This deprecation does not remove scheduling support.

To reject new legacy PodGroup Pods and updates that introduce legacy PodGroup
usage, set `admission.featureGates.Legacyv1alpha1PodGroups: false` (default:
`true`). Pods already using legacy PodGroups can still be updated, with a
deprecation warning, so metadata changes and finalizer removal remain possible.

For compatibility on clusters where the supported built-in PodGroup API is
unavailable, legacy workloads still require the
[PodGroup coscheduling CRD][podgroups-crd] plus the out-of-tree CoScheduling
controller:

```sh
helm install --repo https://scheduler-plugins.sigs.k8s.io scheduler-plugins scheduler-plugins \
  --namespace scheduler-plugins --create-namespace \
  --set 'plugins.enabled={CoScheduling}' --set 'scheduler.replicaCount=0'
```

Pods join the group via the label `scheduling.x-k8s.io/pod-group` (see
[`hack/examples/podgroup-coscheduling/`](../hack/examples/podgroup-coscheduling/)).
Gang size is `spec.minMember` on the PodGroup object.

|                 | Built-in PodGroup                                                         | PodGroup coscheduling                 |
| --------------- | ------------------------------------------------------------------------- | ------------------------------------- |
| API version     | `scheduling.k8s.io/v1alpha2` (1.36) or `scheduling.k8s.io/v1beta1` (1.37) | `scheduling.x-k8s.io/v1alpha1`        |
| Install         | Feature gate + runtime config                                             | CRD + helm chart                      |
| Pod association | `spec.schedulingGroup.podGroupName`                                       | Label `scheduling.x-k8s.io/pod-group` |
| Gang field      | `spec.schedulingPolicy.gang.minCount`                                     | `spec.minMember`                      |

### Migrating from v1alpha1 PodGroups

The built-in and scheduler-plugins PodGroups are separate APIs with different
schemas. Changing only `apiVersion` is not sufficient.

1. Enable the built-in Workload and PodGroup APIs for your cluster as described
   in [Built-in PodGroup](#built-in-podgroup): `scheduling.k8s.io/v1alpha2` on
   Kubernetes **1.36** or `scheduling.k8s.io/v1beta1` on **1.37**.
1. Update new Pod manifests or workload Pod templates to reference the built-in
   PodGroup through `spec.schedulingGroup.podGroupName`. Remove the
   `scheduling.x-k8s.io/pod-group` label; leaving it set continues to trigger
   the deprecation warning.
1. Let existing legacy workloads finish before removing their PodGroups. Keep
   the scheduler-plugins CRD and CoScheduling controller installed while any
   workloads still depend on them.

## LeaderWorkerSet

> [!WARNING]
> Implicit LeaderWorkerSet gang scheduling is deprecated in `slurm-bridge` and
> will be removed in a future release. Admission warns for LWS Pods without
> `spec.schedulingGroup.podGroupName`. Enable
> [native LWS gang scheduling][lws-gang-scheduling] with `spec.scheduling: {}`
> on the LeaderWorkerSet to migrate.

To reject new legacy LWS Pods and updates that introduce implicit LWS gang
scheduling, set `admission.featureGates.LegacyLWSGangs: false` (default:
`true`). Pods already using implicit LWS gang scheduling can still be updated,
with a deprecation warning, so metadata changes and finalizer removal remain
possible.

This section assumes [LeaderWorkerSet][leaderworkersets] is installed.

LeaderWorkerSet groups will be co-scheduled so pods of each group will be
guaranteed to launch together.

Use `spec.groupIdentity: Ordinal` (the LWS default) and
`spec.startupPolicy: LeaderCreated` for this integration. Each replica group
receives its own Slurm allocation, with a default job name of
`<set-name>-<group-index>`. Existing manifests that omit `groupIdentity` retain
ordinal behavior with LWS v0.11.1. The `Hash` identity mode is not currently
supported by slurm-bridge.

The development installer uses LWS v0.11.1 with its default feature gates. Keep
the LWS `WorkloadAwareScheduling` gate disabled when using this direct
LeaderWorkerSet group scheduling. The existing
[built-in PodGroup](#built-in-podgroup) integration can still group LWS Pods
through `spec.schedulingGroup.podGroupName` on their Pod templates.

When upgrading an existing Helm installation, apply the updated LWS CRDs before
upgrading the controller, following the
[upstream upgrade instructions](https://lws.sigs.k8s.io/docs/installation/#upgrade-by-helm).

> [!NOTE]
> Topology-aware placement is not supported yet, so some features of
> LeaderWorkerSet may not behave as expected.

## Scaling and failure behavior

Slurm-bridge does not provide an autoscaler. Workload controllers or external
autoscalers create and remove Pods; the bridge submits, updates, and cancels
their Slurm external jobs. An accepted Kubernetes update does not guarantee that
Slurm can allocate the requested resources.

The allocation boundary matters: a JobSet normally has one Slurm job per Pod,
while direct LeaderWorkerSet scheduling has one per replica group. A Gang
PodGroup takes precedence over those defaults. A Basic PodGroup retains the
normal owner-based allocation boundary. The behavior below applies to each
allocation independently.

### CPU and memory changes

In-place changes to an existing Pod's CPU or memory requests/limits are
unsupported both before and after Slurm allocation. The admission webhook
rejects updates through `pods/resize` with
`can't resize a Slurm Bridge-managed pod`. A Pod still in Kubernetes `Pending`
phase may already have a Slurm allocation, so its phase does not indicate that
resizing is allowed.

Use replacement Pods with the desired resource template. For a JobSet, recreate
the workload as described above. For a LeaderWorkerSet, updating the leader or
worker resource template triggers the LWS controller's rollout, replacing groups
according to its rollout strategy. Replacement groups request new Slurm
allocations; the bridge does not resize the old allocations. This replacement
behavior also applies when the old Pods have not started running yet.

### Changing the number of Pods

| Change                                                                                         | Before Slurm allocates the job                                                                                                                                                | After Slurm allocates the job                                                                                                    |
| ---------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| Change membership of an existing gang                                                          | The scheduler can update the pending Slurm job from the current group on a scheduling retry, subject to workload API validation, gang quorum, and Slurm accepting the update. | Live expansion or shrinking is unsupported. Recreate the group for a new allocation.                                             |
| Change LWS `spec.leaderWorkerTemplate.size`                                                    | LWS rolls out replacement groups; this is not an in-place resize of a queued group.                                                                                           | LWS rolls out replacement groups, each requiring a new allocation.                                                               |
| Add or remove independent Pods, including ungrouped JobSet Pods when the JobSet API permits it | Each added Pod submits its own job; removed Pods have their pending jobs canceled during cleanup.                                                                             | New Pods queue for separate allocations; removed Pods release their own allocations during cleanup. Other allocations continue.  |
| Change LWS `spec.replicas`                                                                     | Supported: add or remove whole independent groups, each with its own Slurm job.                                                                                               | Supported: new groups queue independently; removed groups release their allocations during cleanup. Retained groups keep theirs. |

Gang quorum is `spec.schedulingPolicy.gang.minCount` for built-in PodGroups,
`spec.minMember` for legacy PodGroups, or the configured LWS group size for
implicit LWS gangs. If membership falls below quorum before allocation, the
scheduler waits for enough Pods and cancels an existing job it detects as
invalid. Pending updates are not an atomic scaling operation and can race with
Slurm allocation. Once allocated, treat group membership as fixed: additional
Pods cannot expand that allocation and may remain Pending. For LWS, scale whole
replica groups instead. If templates put multiple LWS replica groups into one
Gang PodGroup, those groups share an allocation and cannot scale independently.

### Scale-down and resource release

Deleting Pods does not partially shrink a running Slurm allocation. The Pod
controller cancels the Slurm job once all Pods associated with that job are
terminal (`Succeeded` or `Failed`). Periodic cleanup also cancels pending or
running jobs whose referenced Pods no longer exist. Removing one independent Pod
therefore releases its allocation, while removing a whole LWS group releases
that group's allocation. Removing only some Pods from a shared job retains its
full allocation while other members remain active.

Cleanup is asynchronous: a deletion request or a Pod in `Terminating` does not
mean Slurm resources are already free. The bridge also removes its Pod finalizer
and generated DRA ResourceClaim during terminal Pod cleanup. Controller or API
errors can delay resource release.

### Failures and recovery

If a scale-up cannot obtain resources, new Pods and their jobs wait for
scheduling; existing independent allocations continue. Admission rejection
leaves the Kubernetes object unchanged. If Slurm rejects a pending job update,
the previous Slurm request remains in effect. Submission or update errors are
reported through scheduling failures and scheduler logs and can be retried. The
bridge does not roll back the workload's desired replica count or templates, and
an API error is not evidence that a job was canceled or resources were released.

A container restart within the same Pod does not by itself release the Slurm
job. Terminal Pods trigger the cleanup described above. If a Slurm job stops
running, for example because it is canceled or reaches its time limit, the Pod
controller deletes its non-terminal, bound Pods. Job retries and JobSet failure
policies determine whether batch Pods are replaced or the JobSet fails. LWS's
`spec.leaderWorkerTemplate.restartPolicy` controls group recovery; the default
`RecreateGroupOnPodRestart` recreates the group on Pod recreation or container
restart. Replacement Pods need scheduling again. Do not rely on individual Pod
replacement to extend or repair an allocated gang; recreate the whole group for
a fresh allocation.

When scaling stalls, inspect Pod scheduling events, JobSet or LWS status, bridge
scheduler and controller logs, and the Slurm job in `squeue` or
`scontrol show job`. The Pod label `scheduler.slinky.slurm.net/slurm-jobid`
identifies its Slurm job. Correct rejected settings or recreate the affected
group, and verify that the old job is no longer pending or running before
assuming its resources have been released.

<!-- Links -->

[jobs]: https://kubernetes.io/docs/concepts/workloads/controllers/job/
[jobsets]: https://jobset.sigs.k8s.io/
[leaderworkersets]: https://lws.sigs.k8s.io/
[lws-gang-scheduling]: https://lws.sigs.k8s.io/docs/concepts/leaderworkerset/gang-scheduling/
[podgroups-crd]: https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/config/crd/bases/scheduling.x-k8s.io_podgroups.yaml
[pods]: https://kubernetes.io/docs/concepts/workloads/pods/
[workload-api]: https://kubernetes.io/docs/concepts/workloads/workload-api/
