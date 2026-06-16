# Config

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Config](#config)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Partitions](#partitions)
  - [Nodes](#nodes)
    - [External Nodes](#external-nodes)
    - [Hybrid Nodes](#hybrid-nodes)
    - [Topology](#topology)
    - [Hybrid Workload Isolation](#hybrid-workload-isolation)

<!-- mdformat-toc end -->

## Overview

When using slurm-bridge, there are some configuration requirements and
considerations to be made.

## Partitions

Slurm bridge external jobs are submitted to a default partition (e.g.
`slurm-bridge`) in Slurm, or the one specified by
`slurmjob.slinky.slurm.net/partition` on the workload. Any partition where these
external jobs are submitted to should only consist of Slurm nodes that map to
Kubernetes nodes, whether they are nodes with co-located kubelet and slurmd or
Kubernetes nodes modeled as Slurm external nodes.

## Nodes

Slurm bridge supports two ways to make Kubernetes nodes available to Slurm:

- **External nodes**: `slurm-bridge` registers labeled Kubernetes nodes with
  Slurm as external Slurm nodes. No slurm-operator `Nodeset` is required for the
  bridge partition.
- **Hybrid nodes**: slurm-operator runs `slurmd` side-by-side with kubelet,
  typically using a DaemonSet-mode `Nodeset`. The `slurmd` pods register Slurm
  dynamic nodes.

In both modes, Slurm should only schedule bridge jobs to Slurm nodes that map
back to Kubernetes nodes.

### External Nodes

External nodes are Kubernetes nodes that `slurm-bridge` registers with Slurm
using `State=External`. The node controller watches for the
`scheduler.slinky.slurm.net/external-node` label and creates or removes the
matching Slurm node.

```yaml
apiVersion: v1
kind: Node
metadata:
  name: worker-1
  labels:
    scheduler.slinky.slurm.net/external-node: "true"
  annotations:
    scheduler.slinky.slurm.net/external-node-partitions: slurm-bridge
```

The `scheduler.slinky.slurm.net/external-node-partitions` annotation is a
comma-separated list of Slurm partitions. The bridge validates that each
partition exists, then registers the Slurm node with matching features. Slurm
`Nodeset` configuration can then map those features into partitions:

```conf
Nodeset=slurm-bridge Feature=slurm-bridge
PartitionName=slurm-bridge Nodes=slurm-bridge State=UP Default=NO
```

### Hybrid Nodes

Hybrid nodes run kubelet and `slurmd` on the same physical host. In this mode,
slurm-operator manages the Slurm nodes with a `Nodeset`, and bridge jobs run on
the Slurm nodes registered by those `slurmd` pods.

For example, a DaemonSet-mode `Nodeset` can place one `slurmd` pod on each
Kubernetes worker node selected for bridge scheduling:

```yaml
nodesets:
  slurm-bridge:
    enabled: true
    scalingMode: DaemonSet
    partition:
      enabled: true
    podSpec:
      nodeSelector:
        scheduler.slinky.slurm.net/slurm-bridge: worker
```

### Topology

Slurm supports dynamic node topology with `topology.yaml`. The topology file
must be available to Slurm. Kubernetes nodes can be annotated with the Slurm
topology units they belong to.

```yaml
configFiles:
  topology.yaml: |
    ---
    - topology: topo-switch
      cluster_default: true
      tree:
        switches:
          - switch: sw_root
            children: s[1-2]
          - switch: s1
            nodes: slurm-node[1-2]
          - switch: s2
            nodes: slurm-node[3-4]
    - topology: topo-block
      cluster_default: false
      block:
        block_sizes:
          - 2
          - 4
        blocks:
          - block: b1
            nodes: slurm-node[1-2]
          - block: b2
            nodes: slurm-node[3-4]
```

Annotate the Kubernetes node with the topology units for the corresponding Slurm
node:

```yaml
apiVersion: v1
kind: Node
metadata:
  name: worker-1
  annotations:
    topology.slinky.slurm.net/spec: topo-switch:s1,topo-block:b1
```

External and hybrid modes use the same Kubernetes annotation, but different
controllers apply it to Slurm:

- In external mode, `slurm-bridge` reads `topology.slinky.slurm.net/spec` from
  the labeled Kubernetes node and reconciles the external Slurm node's dynamic
  topology.
- In hybrid mode, slurm-operator reads `topology.slinky.slurm.net/spec` from the
  Kubernetes node where the `Nodeset` pod is scheduled, copies it to the pod,
  and reconciles the `slurmd` pod's Slurm node topology.

Removing the annotation clears the dynamic topology in either mode. When using
multiple topologies, Slurm partitions can select a specific topology with the
partition `Topology` setting; otherwise Slurm uses the `cluster_default`
topology.

### Hybrid Workload Isolation

When you have kubelet and slurmd daemons running side-by-side, you may want to
isolate their workloads to allow the physical node to dynamically switch
workload types.

This can be achieved by enabling [MCS] in Slurm.

```conf
# slurm.conf
...
MCSPlugin=mcs/label
MCSParameters=ondemand,ondemandselect
```

<!-- Links -->

[mcs]: https://slurm.schedmd.com/mcs.html
