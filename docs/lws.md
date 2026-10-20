# LeaderWorkerSet

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [LeaderWorkerSet](#leaderworkerset)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Scaling](#scaling)
  - [Failures and recovery](#failures-and-recovery)

<!-- mdformat-toc end -->

## Overview

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

This guide assumes [LeaderWorkerSet][leaderworkersets] is installed.

LeaderWorkerSet groups will be co-scheduled so pods of each group will be
guaranteed to launch together.

Use `spec.groupIdentity: Ordinal` (the LWS default) and
`spec.startupPolicy: LeaderCreated` for this integration. Each replica group
receives its own Slurm allocation, with a default job name of
`<set-name>-<group-index>`. Existing manifests that omit `groupIdentity` retain
ordinal behavior with LWS v1.0.0. The `Hash` identity mode is not currently
supported by slurm-bridge.

The development installer uses LWS v1.0.0 with its default feature gates. Keep
the LWS `WorkloadAwareScheduling` gate disabled when using this direct
LeaderWorkerSet group scheduling. The existing [built-in PodGroup](podgroup.md)
integration can still group LWS Pods through `spec.schedulingGroup.podGroupName`
on their Pod templates.

When upgrading an existing Helm installation, apply the updated LWS CRDs before
upgrading the controller, following the
[upstream upgrade instructions](https://lws.sigs.k8s.io/docs/installation/#upgrade-by-helm).

> [!NOTE]
> Topology-aware placement is not supported yet, so some features of
> LeaderWorkerSet may not behave as expected.

See [Workloads](workload.md) for shared scheduler and resource configuration.

## Scaling

In-place Pod CPU and memory changes are unsupported. Updating the leader or
worker resource template triggers the LWS controller's rollout, replacing groups
according to its rollout strategy. Replacement groups request new Slurm
allocations; the bridge does not resize the old allocations. This also applies
when the old Pods have not started running yet.

Changing `spec.leaderWorkerTemplate.size` rolls out replacement groups both
before and after Slurm allocation. It does not resize an existing queued or
running group in place.

Changing `spec.replicas` adds or removes whole independent groups, each with its
own Slurm job. New groups queue independently; removed groups release their
allocations during asynchronous cleanup. Retained groups keep their allocations.
Removing only some Pods from a shared job retains its full allocation while
other members remain active.

The quorum for an implicit LWS gang is the configured group size. If templates
put multiple LWS replica groups into one Gang PodGroup, those groups share an
allocation and cannot scale independently. Follow the
[gang scaling limits](workload.md#changing-the-number-of-pods) for that
allocation.

## Failures and recovery

LWS's `spec.leaderWorkerTemplate.restartPolicy` controls group recovery; the
default `RecreateGroupOnPodRestart` recreates the group on Pod recreation or
container restart. Replacement Pods need scheduling again. Do not rely on
individual Pod replacement to extend or repair an allocated gang; recreate the
whole group for a fresh allocation.

See [Scaling and failure behavior](workload.md#scaling-and-failure-behavior) for
shared admission, Slurm update, cleanup, and troubleshooting behavior.

<!-- Links -->

[leaderworkersets]: https://lws.sigs.k8s.io/
[lws-gang-scheduling]: https://lws.sigs.k8s.io/docs/concepts/leaderworkerset/gang-scheduling/
