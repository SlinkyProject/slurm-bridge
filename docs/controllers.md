# Controllers

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Controllers](#controllers)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Node Controller](#node-controller)
  - [Workload Controller](#workload-controller)

<!-- mdformat-toc end -->

## Overview

This component is comprised of multiple controllers with specialized tasks.

## Node Controller

The node controller is responsible for tainting the managed nodes so the
scheduler component is fully in control of all workload that is bound to those
nodes.

Additionally, this controller will reconcile certain node states for scheduling
purposes. Slurm becomes the source of truth for scheduling among managed nodes.

A managed node is defined as a node that has a colocated `kubelet` and `slurmd`
on the same physical host, and the slurm-bridge can schedule on.

```{mermaid}
sequenceDiagram
  autonumber

  participant KAPI as Kubernetes API
  participant SWC as Slurm Workload Controller
  participant SAPI as Slurm REST API

  loop Reconcile Loop
    KAPI-->>SWC: Watch Kubernetes Nodes

    alt Node is managed
      SWC->>KAPI: Taint Node
      KAPI-->>SWC: Taint Node
    else
      SWC->>KAPI: Untaint Node
      KAPI-->>SWC: Untaint Node
    end %% alt Node is managed

    alt Node is schedulable
      SWC->>SAPI: Drain Node
      SAPI-->>SWC: Taint Node
    else
      SWC->>SAPI: Undrain Node
      SAPI-->>SWC: Undrain Node
    end %% alt Node is schedulable

  end %% loop Reconcile Loop
```

## Workload Controller

The workload controller reconciles Kubernetes Pods and Slurm Jobs. Slurm is the
source of truth for what workload is allowed to run on which managed nodes.

```{mermaid}
sequenceDiagram
  autonumber

  participant KAPI as Kubernetes API
  participant SWC as Slurm Workload Controller
  participant SAPI as Slurm REST API

  loop Reconcile Loop

  critical Map Slurm Job to Pod
    KAPI-->>SWC: Watch Kubernetes Pods
    SAPI-->>SWC: Watch Slurm Jobs
  option Pod is Terminated
    SWC->>SAPI: Terminate Slurm Job
    SAPI-->>SWC: Return Status
  option Job is Terminated
    SWC->>KAPI: Evict Pod
    KAPI-->>SWC: Return Status
  end %% critical Map Slurm Job to Pod

  end %% loop Reconcile Loop
```
