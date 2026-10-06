# Config

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Config](#config)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Partitions](#partitions)
  - [Converged/Hybrid](#convergedhybrid)
    - [Hybrid Workload Isolation](#hybrid-workload-isolation)
      - [Production label authorization](#production-label-authorization)

<!-- mdformat-toc end -->

## Overview

When using slurm-bridge, there are some configuration requirements and
considerations to be made.

## Partitions

Slurm bridge external jobs are submitted to a default partition (e.g.
`slurm-bridge`) in Slurm, or the one specified by
`slurmjob.slinky.slurm.net/partition` on the workload. Any partition where these
external jobs are submitted to should only consist of Slurm nodes that map to
Kubernetes nodes, be them nodes with co-located kubelet and slurmd, or
Kubernetes nodes modeled as Slurm external nodes.

## Converged/Hybrid

Hybrid nodes share capacity between workload managers over time. A physical node
must not run a native Slurm user workload and a Slurm-bridge-managed Kubernetes
user workload simultaneously. System components such as kubelet, `slurmd`, CNI,
device plugins, and monitoring DaemonSets are expected exceptions.

### Hybrid Workload Isolation

Bridge jobs receive exclusive whole-node Slurm allocations by default. Workloads
requesting `slurmjob.slinky.slurm.net/exclusive: "false"` are always submitted
with `Shared=mcs` and the configured `schedulerConfig.mcsLabel`. This allows
bridge-managed Kubernetes workloads in the same MCS category to share a node
without enabling unprotected sharing with native Slurm jobs.

Configure both the bridge and Slurm to enable [MCS] isolation:

```yaml
# slurm-bridge values.yaml
schedulerConfig:
  mcsLabel: kubernetes
```

```conf
# slurm.conf
MCSPlugin=mcs/label
MCSParameters=ondemand,ondemandselect
```

With `ondemandselect`, the bridge's `Shared=mcs` value activates MCS node
filtering for non-exclusive jobs. Native Slurm jobs with a different or empty
MCS label cannot share those nodes while a `kubernetes`-labeled allocation is
running.

#### Production label authorization

Slurm's `mcs/label` plugin controls category-based sharing but accepts arbitrary
labels; it does not authorize their use. Production clusters must reserve
`schedulerConfig.mcsLabel` at the `slurmctld` boundary with a server-side
[`job_submit` plugin][job-submit] or equivalent site policy, using a trusted
bridge identity and rejecting native submissions or modifications that request
the reserved label. Without that Slurm-side policy, a native user can
deliberately join the bridge's MCS category.

MCS only governs workloads represented by active Slurm allocations. Continue to
use the `slinky.slurm.net/managed-node` `NoExecute` taint and admission policy
to keep Kubernetes workloads that bypass slurm-bridge off hybrid nodes.
Operational or controller-driven cancellation must also keep a node unavailable
to native Slurm work until its Kubernetes pods have actually stopped.

<!-- Links -->

[job-submit]: https://slurm.schedmd.com/job_submit_plugins.html
[mcs]: https://slurm.schedmd.com/mcs.html
