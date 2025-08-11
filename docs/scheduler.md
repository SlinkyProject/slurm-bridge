# Scheduler

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Scheduler](#scheduler)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
    - [Sequence Diagram](#sequence-diagram)

<!-- mdformat-toc end -->

## Overview

The scheduler controller is responsible for [scheduling] pending pods onto
nodes.

This scheduler is designed to be a non-primary scheduler (e.g. should not
replace the default [kube-scheduler]). This means that only certain pods should
be scheduled via this scheduler (e.g. non-critical pods).

This scheduler represents Kubernetes Pods as a Slurm Job, waits for Slurm to
schedule the Job, then informs Kubernetes on which nodes to allocate the
represented Pods. This scheduler defers scheduling decisions to Slurm, hence
certain assumptions about the environment must be met for this to function
correctly.

### Sequence Diagram

```{mermaid}
sequenceDiagram
  autonumber

  actor user as User
  participant KAPI as Kubernetes API
  participant SBS as Slurm-Bridge Scheduler
  participant SAPI as Slurm REST API

  loop Workload Submission
    user->>KAPI: Submit Pod
    KAPI-->>user: Return Request Status
  end %% loop Workload Submission

  loop Scheduling Loop
    SBS->>KAPI: Get Next Pod in Workload Queue
    KAPI-->>SBS: Return Next Pod in Workload Queue

    note over SBS: Honor Slurm scheduling decision
    critical Lookup Slurm Placeholder Job
      SBS->>SAPI: Get Placeholder Job
      SAPI-->>SBS: Return Placeholder Job
    option Job is NotFound
      note over SBS: Translate Pod(s) into Slurm Job
      SBS->>SAPI: Submit Placeholder Job
      SAPI-->>SBS: Return Submit Status
    option Job is Pending
      note over SBS: Check again later...
      SBS->>SBS: Requeue
    option Job is Allocated
      note over SBS: Bind Pod(s) to Node(s) from the Slurm Job
      SBS->>KAPI: Bind Pod(s) to Node(s)
      KAPI-->>SBS: Return Bind Request Status
    end %% Lookup Slurm Placeholder Job
  end %% loop Scheduling Loop
```

<!-- Links -->

[kube-scheduler]: https://kubernetes.io/docs/concepts/architecture/#kube-scheduler
[scheduling]: https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/#scheduling
