# Config

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Config](#config)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Partitions](#partitions)
  - [Converged/Hybrid](#convergedhybrid)

<!-- mdformat-toc end -->

## Overview

When using slurm-bridge, there are some configuration requirements and
considerations to be made.

## Partitions

Slurm bridge placeholder jobs are submitted to a default partition (e.g.
`slurm-bridge`) in Slurm, or the one specified by
`slurmjob.slinky.slurm.net/partition` on the workload. Any partition where these
placeholder jobs are submitted to should only consist of Slurm nodes that map to
Kubernetes nodes, be them nodes with co-located kubelet and slurmd, or
Kubernetes nodes modeled as Slurm external nodes.

## Converged/Hybrid

When you have kubelet and slurmd daemons running side-by-side, you may want to
isolate their workloads to allow the physical node to dynamically switch
workloads types.

This can be achieved by enabling [MCS] in Slurm.

```conf
# slurm.conf
...
MCSPlugin=mcs/label
MCSParameters=ondemand,ondemandselect
```

<!-- Links -->

[mcs]: https://slurm.schedmd.com/mcs.html
