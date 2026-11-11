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
  - [NUMA alignment](#numa-alignment)
  - [Annotations](#annotations)
    - [Resolution rules](#resolution-rules)
    - [Supported Slurm job annotations](#supported-slurm-job-annotations)
    - [Scheduler-managed Pod metadata](#scheduler-managed-pod-metadata)
  - [Pod grouping](#pod-grouping)
    - [Other controller owners](#other-controller-owners)
  - [Workload guides](#workload-guides)
  - [Workload Preemption](#workload-preemption)
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
[JobSets](jobset.md), [Pods], [built-in PodGroup](podgroup.md)
(`scheduling.k8s.io/v1alpha2` on Kubernetes **1.36**, or
`scheduling.k8s.io/v1beta1` on **1.37**),
[PodGroup coscheduling](podgroup-coscheduling.md) (scheduler-plugins,
deprecated), and [LeaderWorkerSets](lws.md). If your workload requires or
benefits from co-scheduled pod launch (e.g. MPI, multi-node), use
[built-in PodGroup](podgroup.md) on Kubernetes **1.36 or 1.37** when its API is
enabled. The deprecated [PodGroup coscheduling](podgroup-coscheduling.md)
integration remains available for compatibility when the built-in API is
unavailable.

## Using the `slurm-bridge` Scheduler

`slurm-bridge` uses an
[admission controller](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/)
to control which resources are scheduled using the `slurm-bridge-scheduler`. The
`slurm-bridge-scheduler` is designed as a non-primary scheduler and is not
intended to replace the default
[kube-scheduler](https://kubernetes.io/docs/concepts/architecture/#kube-scheduler).
The `slurm-bridge-scheduler` only schedules pods whose `spec.schedulerName`
matches its configured name, `slurm-bridge-scheduler` by default. In managed
namespaces, admission changes `default-scheduler` to that name; explicitly
selected custom schedulers are preserved.

Alternatively, a pod can specify `Pod.Spec.schedulerName=slurm-bridge-scheduler`
from an unmanaged namespace to select the `slurm-bridge-scheduler`. The Helm
webhooks exclude `kube-system` and the bridge's installation namespace, so use a
separate namespace for bridge workloads.

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
and limits in both init containers and regular containers. For NVIDIA GPUs and
MIG devices, see [NVIDIA GPUs and MIG with DRA](nvidia-gpu-dra-mig.md).

The bridge keeps `DRADeviceTaintRules` disabled by default because clusters
older than Kubernetes 1.37 do not serve `resource.k8s.io/v1` DeviceTaintRules.
Slurm chooses the devices, so Kubernetes device taints do not stop Slurm from
allocating a tainted device.

Configuration from each matching `DeviceClass.spec.config` is copied into the
generated ResourceClaim allocation with source `FromClass` and scoped to the
requests which use that class. Claim configuration is retained with source
`FromClaim` after the class configuration. This preserves opaque driver
configuration such as network attachment parameters while keeping allocation
decisions under Slurm's control.

Indexed DRA devices are mapped to Slurm GRES through `deviceProfiles` in the
shared Slurm Bridge configuration. The built-in profiles cover CPU, NVIDIA GPU,
and PCI-backed DRANET devices. The example GPU driver requires explicit
configuration. Setting `sharedConfig.deviceProfiles` replaces the built-in list;
include the built-in entries too if they are still needed. For example, this
configuration enables only the custom accelerator profile:

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

Enable DRANET's `PersistentResourceSliceAttributes` feature gate to retain
allocated devices and their attributes in ResourceSlices. It is alpha and
disabled by default in DRANET v1.5.0; `hack/kind.sh --dranet` enables it
automatically. For the DRANET v1.5.0 Helm chart, set:

```yaml
args:
  featureGates: PersistentResourceSliceAttributes=true
```

Append any other enabled DRANET gates to the same comma-separated value. Without
persistence, moving a NIC into a Pod can remove the device or its
profile-matching attributes from ResourceSlices. The bridge then drains the
external Slurm node for re-registration: `scontrol show node <node>` shows
`DRAIN` and a reason containing `removing external node`, while unrelated jobs
remain pending despite free CPUs. If this coincides with NIC allocation and
clears on release, check the DRANET feature gate and ResourceSlice inventory.

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
`cpuDeviceMode: grouped`, `groupBy: numanode` or `groupBy: socket`, and
`allocator: external`. Use `numanode` for [NUMA alignment](#numa-alignment).
After reserved CPUs are excluded, groups (NUMA domains or physical sockets) must
have equal core counts and cores must have equal thread counts. Slurm must be
the sole allocator for these devices; kubelet's CPU Manager must not assign the
same CPUs independently.

To migrate from individual CPU devices, drain Slurm nodes and wait for all
claims to be unprepared before replacing the driver and bridge. Use the same
procedure before changing CPU reservations or topology. Resume work after Slurm
registration matches the new inventory. Hybrid nodes also require matching
slurmd geometry; geometry checks do not verify Linux CPU ordering on physical
hardware.

## NUMA alignment

`slurm-bridge` binds each DRA device to the Slurm cores of its NUMA node, so
Slurm can place a job's CPUs, GPUs, and NICs on the same NUMA node. This
requires:

- [CPU DRA](#cpu-dra) with `groupBy: numanode`.
- Kubernetes 1.37 or newer with the `DRAListTypeAttributes` feature gate on the
  API server, scheduler, controller manager, and kubelet.
- DRA drivers which publish `resource.kubernetes.io/numaNode`, such as the
  NVIDIA GPU driver and DRANET with their `DRAListTypeAttributes` feature gate.

Alignment is best effort: Slurm prefers local cores, but a job which needs more
than one NUMA node still runs. With NUMA-grouped CPU inventory, each NUMA domain
is registered as a Slurm socket. This preserves device locality when several
NUMA domains share a physical socket, including sub-NUMA clustering. Abstract
Slurm cores are ordered by physical socket, NUMA domain, and then core; each
domain occupies a contiguous range.

When changing from physical-socket registration to NUMA domains as sockets,
drain nodes and wait for claims to be unprepared. External nodes are
re-registered with the new geometry before accepting bridge jobs.

Hybrid nodes are aligned only when the site's `gres.conf` describes the same
topology; `slurm-bridge` does not write it, and the `SlinkySlurmGRESCompatible`
condition checks only GRES names, types, and counts. For each GRES type, list
devices in the string order of their DRA device names (`gpu-10` before `gpu-2`),
give each the `Cores=` of its NUMA domain, and set slurmd's `Sockets` and
`CoresPerSocket` to match CPU DRA. For sub-NUMA clustering, configure
`SlurmdParameters=numa_node_as_socket` and update the node geometry to match
`slurmd -C --parameters=numa_node_as_socket`. Geometry mismatches prevent bridge
jobs from using the node. Verify Linux CPU ordering against the bridge mapping
as well; geometry checks alone cannot establish it. Incorrect GRES locality can
still let jobs run with devices and CPUs from different NUMA domains.

## Annotations

Users can influence how `slurm-bridge` represents their Kubernetes workload in
Slurm by adding `slurmjob.slinky.slurm.net/*` annotations to Pods, including
through workload Pod templates. They configure the Slurm external job and are
separate from [scheduler-managed metadata](#scheduler-managed-pod-metadata).

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
spec:
  template:
    metadata:
      annotations:
        slurmjob.slinky.slurm.net/timelimit: "5"
        slurmjob.slinky.slurm.net/account: foo
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
Slurm defaults -> values derived from the workload and Pods -> legacy workload
annotations -> Pod annotations**. Pod annotations always win. Values are merged
before parsing, so an overridden legacy value is ignored even if it is invalid.

For built-in PodGroups, legacy annotation precedence remains **selected workload
owner -> PodGroup -> Workload**, followed by Pod annotations. Keys that do not
conflict are combined. The first Pod in each Slurm component supplies the Pod
annotations. Pods within a component must have matching Slurm annotations;
consistency is not checked. For CompositePodGroup gangs, each leaf PodGroup has
its own component and can use different annotations from sibling PodGroups.

Top-level workload annotations are being deprecated and remain defaults during
the transition; support will be removed in a future release. Admission warns
about them when known workloads are created in managed namespaces by default; it
can also [reject them](admission.md#workload-annotation-deprecation). Move them
to Pod template metadata; for LeaderWorkerSets, use the leader and worker
templates as appropriate.

### Supported Slurm job annotations

| Purpose                 | Annotation suffixes                                                                   |
| ----------------------- | ------------------------------------------------------------------------------------- |
| Identity and accounting | `account`, `group-id`, `user-id`, `wckey`                                             |
| Scheduling policy       | `constraints`, `exclusive`, `licenses`, `partition`, `priority`, `qos`, `reservation` |
| Resources               | `cpu-per-task`, `gres`, `max-nodes`, `mem-per-node`, `min-nodes`                      |
| Naming and duration     | `job-name`, `timelimit`                                                               |

Prefix every suffix with `slurmjob.slinky.slurm.net/`; for example,
`slurmjob.slinky.slurm.net/account`. Node counts and priority are base-10
integers. CPU and memory accept Kubernetes quantities. `gres` is passed to
Slurm's `tres_per_node` field; use a value such as `gres/gpu=2`. `timelimit`
accepts a duration with a unit suffix (`s`, `m`, `h`, `d`, `w`), one of Slurm's
`--time` formats (`MM:SS`, `HH:MM:SS`, `D-HH`, `D-HH:MM`, `D-HH:MM:SS`), or a
bare integer, which is read as minutes; sub-minute values are rounded up to one
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

The `user-id`, `group-id`, `account`, and `exclusive` annotations apply only at
submission. Changing them does not modify an existing Slurm job; submit a new
workload to use different values. Bridge preserves the submitted ownership and
isolation when updating the job through its privileged Slurm identity.

Other annotations can update a Slurm job while it is pending. Slurm validates
each change, but a failed update can still have partially applied changes.
Removing an annotation does not necessarily reset the stored Slurm value. Once
Slurm allocates the job, treat its annotations as fixed. Pod membership is fixed
at submission, as described in
[Changing the number of Pods](#changing-the-number-of-pods).

### Scheduler-managed Pod metadata

Slurm-bridge writes the following metadata to Pods as it creates and allocates
the external job:

| Metadata                                 | Type       | Meaning                                                                                                |
| ---------------------------------------- | ---------- | ------------------------------------------------------------------------------------------------------ |
| `scheduler.slinky.slurm.net/slurm-jobid` | Label      | Slurm job ID associated with the Pod. All Pods represented by one external job receive the same value. |
| `slinky.slurm.net/slurm-node`            | Annotation | Kubernetes node selected from the Slurm allocation for this Pod.                                       |

These keys are owned by slurm-bridge and must not be edited manually. Admission
rejects non-empty values when creating a managed Pod. Subsequent updates are not
rejected on that basis; the Pod controller repairs metadata using the verified
Slurm allocation.

## Pod grouping

Slurm-bridge turns each group of Kubernetes Pods into one Slurm external job:

| Workload              | Pods in one external job                                 | Where to put annotations |
| --------------------- | -------------------------------------------------------- | ------------------------ |
| Pod                   | That Pod                                                 | Pod metadata             |
| Job or JobSet         | One Pod                                                  | Pod template metadata    |
| Built-in PodGroup     | Pods with the same `spec.schedulingGroup.podGroupName`   | Member Pod metadata      |
| PodGroup coscheduling | Pods with the same `scheduling.x-k8s.io/pod-group` label | Member Pod metadata      |
| LeaderWorkerSet       | One LeaderWorkerSet group                                | Pod template metadata    |
| Other controller      | One Pod                                                  | Pod template metadata    |

The grouping precedence is **built-in PodGroup -> PodGroup coscheduling ->
supported workload owner -> the Pod itself**. Native PodGroups use their
explicit Workload reference and CompositePodGroup parent references.

Legacy owner lookups follow only Pod -> Job -> JobSet and the defined
LeaderWorkerSet paths, directly or through StatefulSets and leader Pods. The
selected Job, JobSet, or LeaderWorkerSet supplies defaults; intermediate objects
do not. Native PodGroups also read their explicit PodGroup and Workload
defaults. Coscheduling PodGroups retain their PodGroup defaults.

### Other controller owners

Other controller references are not fetched or traversed. Deployment/ReplicaSet,
RayCluster, and custom controller chains use Pod annotations and schedule one
external job per Pod. For Ray, place annotations on the head and worker Pod
templates; RayCluster metadata is not read. Unknown owners above supported
workloads are ignored. Errors reading objects on a defined path still fail
scheduling.

## Workload guides

- [Built-in PodGroup](podgroup.md): Workload and PodGroup APIs, gang scheduling,
  and Kubernetes version upgrades.
- [JobSets](jobset.md): per-Pod allocations, scaling restrictions, and recovery.
- [LeaderWorkerSet](lws.md): group scheduling, replica scaling, and recovery.
- [NVIDIA GPUs and MIG with DRA](nvidia-gpu-dra-mig.md): driver installation,
  MIG device profiles, and allocation checks.
- [TPU DRA](tpu.md): per-Pod whole-host requests and a fake-device Kind check.
- [PodGroup coscheduling](podgroup-coscheduling.md): legacy scheduler-plugins
  integration and migration to built-in PodGroups.

## Workload Preemption

`PreemptMode=REQUEUE` and `PreemptMode=CANCEL` currently have the same effect on
`slurm-bridge` workloads. Slurm terminates the external job rather than
requeueing it, and the bridge deletes its non-terminal, bound Pods. Pod
termination is asynchronous.

Workload controllers determine whether Pods are replaced:

- Standalone Pods and PodGroup or CompositePodGroup members without an owning
  workload controller are not recreated.
- Jobs and JobSets recover according to Job retry budgets and JobSet failure
  policies. Preempted Pods count as failed attempts, so `backoffLimit: 0` can
  cause the workload to fail.
- LeaderWorkerSets recreate groups according to their
  [group recovery policy](lws.md#failures-and-recovery).

Replacement Pods need scheduling again and request new Slurm allocations.
Slurm-bridge does not resume the preempted Pods or requeue their original
allocations.

## Scaling and failure behavior

Workload controllers or external autoscalers create and remove Pods; the bridge
submits, updates, and cancels their Slurm external jobs. An accepted Kubernetes
update does not guarantee that Slurm can allocate the requested resources.

A JobSet normally has one Slurm job per Pod, while direct LeaderWorkerSet
scheduling has one per replica group. A Gang PodGroup takes precedence over
those defaults. A Basic PodGroup retains the normal owner-based allocation
boundary. The behavior below applies to each allocation independently.

### CPU and memory changes

In-place changes to an existing Pod's CPU or memory requests/limits are
unsupported both before and after Slurm allocation. The admission webhook
rejects updates through `pods/resize` with
`can't resize a Slurm Bridge-managed pod`. A Pod still in Kubernetes `Pending`
phase may already have a Slurm allocation, so its phase does not indicate that
resizing is allowed.

Use replacement Pods with the desired resource template. Replacement Pods or
groups request new Slurm allocations; the bridge does not resize the old
allocations. This also applies when the old Pods have not started running yet.
See the [JobSet](jobset.md#scaling) and [LeaderWorkerSet](lws.md#scaling) guides
for workload-specific replacement behavior.

### Changing the number of Pods

| Change                                                                                                       | Before Slurm allocates the job                                                                    | After Slurm allocates the job                                                                                                   |
| ------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| Change membership of an existing gang                                                                        | Membership is fixed when the Slurm job is submitted. Later Pods cannot join the pending job.      | Live expansion or shrinking is unsupported. Recreate the group for a new allocation.                                            |
| Add or remove independent Pods, including ungrouped [JobSet Pods](jobset.md#scaling) when the API permits it | Each added Pod submits its own job; removed Pods have their pending jobs canceled during cleanup. | New Pods queue for separate allocations; removed Pods release their own allocations during cleanup. Other allocations continue. |

Gang quorum is `spec.schedulingPolicy.gang.minCount` for built-in PodGroups,
`spec.minMember` for legacy PodGroups, or the configured LWS group size for
[implicit LWS gangs](lws.md#scaling). A CompositePodGroup also applies
`spec.schedulingPolicy.gang.minGroupCount` at each level of its hierarchy.
Quorum is a readiness threshold, not a cap: submission records the Pod UIDs and
child groups present in the submitted batch.

Later Pods and child groups wait for the current allocation to finish and a new
batch to meet quorum. They cannot join or block its original members. The
controller reports `WaitingForNextAllocation`; a long-running allocation can
leave late arrivals waiting indefinitely. Concurrent batches under one gang
identity are unsupported. Set quorum to the intended total when all members must
share an allocation.

For LWS, [scale whole replica groups](lws.md#scaling). Replica groups combined
into one Gang PodGroup share an allocation and cannot scale independently.

### Scale-down and resource release

Deleting Pods does not partially shrink a running Slurm allocation. The
registration controller cancels the Slurm job once all Pods associated with that
job are terminal (`Succeeded` or `Failed`) or are being deleted before binding
to a node. Bound Pods must terminate before their allocation is released.
Periodic cleanup also cancels pending or running jobs whose referenced Pods no
longer exist. Removing one independent Pod therefore releases its allocation,
while removing a whole LWS group releases that group's allocation. Removing only
some Pods from a shared job retains its full allocation while other members
remain active.

Cleanup is asynchronous: a deletion request or a Pod in `Terminating` does not
mean Slurm resources are already free. The bridge also removes its Pod finalizer
and generated DRA ResourceClaim during terminal Pod cleanup.

### Failures and recovery

If a scale-up cannot obtain resources, new Pods and their jobs wait for
scheduling; existing independent allocations continue. Admission rejection
leaves the Kubernetes object unchanged. If Slurm rejects a pending job update,
the previous Slurm request remains in effect. Submission errors are reported by
the registration controller through workload events and controller logs; update
errors appear in scheduling failures and scheduler logs. Both can be retried.
The bridge does not roll back the workload's desired replica count or templates,
and an API error is not evidence that a job was canceled or resources were
released.

A container restart within the same Pod does not by itself release the Slurm
job. Terminal Pods trigger the cleanup described above. If a Slurm job stops
running, for example because it is canceled or reaches its time limit, the
registration controller deletes its non-terminal, bound Pods. Workload
controllers determine whether Pods are replaced; see
[JobSet recovery](jobset.md#failures-and-recovery) and
[LeaderWorkerSet recovery](lws.md#failures-and-recovery). Replacement Pods need
scheduling again. Do not rely on individual Pod replacement to extend or repair
an allocated gang; recreate the whole group for a fresh allocation.

When a Pod remains `SchedulingGated`, inspect its `SlurmRegistrationPending`
event for the group to check. Registration events such as `WaitingForMembers`
and `SlurmRegistrationFailed` are recorded on that group, including the parent
CompositePodGroup for composite workloads. Also inspect the controllers' logs.

When scaling stalls, inspect Pod scheduling events, JobSet or LWS status, bridge
scheduler and controller logs, and the Slurm job in `squeue` or
`scontrol show job`. The Pod label `scheduler.slinky.slurm.net/slurm-jobid`
identifies its Slurm job. Correct rejected settings or recreate the affected
group, and verify that the old job is no longer pending or running before
assuming its resources have been released.

<!-- Links -->

[jobs]: https://kubernetes.io/docs/concepts/workloads/controllers/job/
[pods]: https://kubernetes.io/docs/concepts/workloads/pods/
