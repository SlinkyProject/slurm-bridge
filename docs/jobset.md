# JobSets

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [JobSets](#jobsets)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Scaling](#scaling)
  - [Failures and recovery](#failures-and-recovery)

<!-- mdformat-toc end -->

## Overview

This guide assumes [JobSets] is installed. See [Workloads](workload.md) for
shared scheduler and resource configuration.

JobSet pods are scheduled on a per-pod basis unless a
[Gang PodGroup](podgroup.md) groups them into a shared Slurm allocation. The Job
and JobSet controllers manage retries, failure policies, and completion;
slurm-bridge manages the corresponding Slurm jobs.

## Scaling

JobSet API validation also limits scaling. In JobSet v0.13.0, changing
`spec.replicatedJobs[].replicas` or the Pod resource template is rejected and
requires recreating the JobSet. The upstream `ElasticJobSet` feature gate
(disabled by default) permits changing `parallelism` and `completions` together
for non-terminal Indexed Jobs, with equal values. Without gang grouping, each
additional Pod gets an independent Slurm job. With gang grouping, the allocation
limitations in [Workloads](workload.md#changing-the-number-of-pods) still apply,
even if the JobSet API accepts the update.

In-place Pod CPU and memory changes are unsupported. Recreate the JobSet with
the desired resource template so replacement Pods request new Slurm allocations.
This also applies when the old Pods have not started running yet.

When the JobSet API permits adding independent Pods, each added Pod submits its
own Slurm job. Removed Pods release their own allocations during asynchronous
cleanup; other independent allocations continue. Gang members share an
allocation and follow the
[gang scaling limits](workload.md#changing-the-number-of-pods).

## Failures and recovery

Job retries and JobSet failure policies determine whether batch Pods are
replaced or the JobSet fails. Replacement Pods need scheduling again. Do not
rely on individual Pod replacement to extend or repair an allocated gang;
recreate the whole group for a fresh allocation.

See [Scaling and failure behavior](workload.md#scaling-and-failure-behavior) for
shared admission, Slurm update, cleanup, and troubleshooting behavior.

<!-- Links -->

[jobsets]: https://jobset.sigs.k8s.io/
