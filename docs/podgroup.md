# Built-in PodGroup

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Built-in PodGroup](#built-in-podgroup)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Upgrading from Kubernetes 1.36 to 1.37](#upgrading-from-kubernetes-136-to-137)

<!-- mdformat-toc end -->

## Overview

See [Workloads](workload.md) for shared scheduler and resource configuration.

`slurm-bridge` supports built-in Workload and PodGroup APIs on Kubernetes **1.36
and 1.37**. Enable the **`GenericWorkload`** feature gate and the API version
for your cluster:

| Kubernetes | Workload and PodGroup `apiVersion` | Kind configuration                              |
| ---------- | ---------------------------------- | ----------------------------------------------- |
| 1.36       | `scheduling.k8s.io/v1alpha2`       | [`hack/kind-1.36.yaml`](../hack/kind-1.36.yaml) |
| 1.37       | `scheduling.k8s.io/v1beta1`        | [`hack/kind.yaml`](../hack/kind.yaml)           |

The **`GenericWorkload`** feature gate is enabled by default in the bridge. It
controls both native group scheduling and bridge Workload registration. At
startup, the scheduler requires both Workload and PodGroup resources in a
supported API version, preferring `v1beta1` when both versions are available.
Missing APIs, incomplete discovery responses, and discovery errors fail startup;
the scheduler does not silently disable native scheduling.

The scheduler's shared PodGroup client and informers use the discovered API
version. Alpha objects and status conditions are converted to the scheduler's
beta types; status writes are converted back to the served version. Helm enables
native group scheduling on Kubernetes 1.36 and newer, and enables the separate
CompositePodGroup and topology hierarchy gates only on Kubernetes 1.37 and
newer. Cluster components must also enable `GenericWorkload` and serve the API
version shown in the Kind configurations above.

Standalone binaries on clusters without these APIs, including Kubernetes
**1.35**, must opt out with `--feature-gates=GenericWorkload=false`. Helm
disables this gate automatically on Kubernetes 1.35; on other clusters it can be
disabled with `scheduler.featureGates.GenericWorkload=false`. The scheduler then
skips Workload discovery and registration. Pods that reference a built-in
PodGroup through `spec.schedulingGroup.podGroupName` are rejected with a clear
scheduling failure before Slurm operations. Ordinary Pods, Jobs, JobSets,
LeaderWorkerSets, and scheduler-plugins PodGroups retain their existing behavior
when they do not reference a built-in PodGroup.

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

The bridge rejects PodGroups with non-empty
`spec.schedulingConstraints.topology` or `spec.resourceClaims`. Use Slurm
partitions or constraints for placement and DeviceClass extended resources on
Pods for DRA devices.

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
manifests in
[`hack/examples/workload/`](https://github.com/SlinkyProject/slurm-bridge/tree/main/hack/examples/workload)
use this 1.36 form.

Ref: [Workload API][workload-api]

To override Slurm submission parameters, add optional
`slurmjob.slinky.slurm.net/*` annotations on the **Workload**, selected
controller (**Job** or **JobSet**), or runtime **PodGroup**. On conflict,
**Workload** > **PodGroup** > **selected controller**. Without them, the Slurm
job name for a Gang policy defaults to the **PodGroup object name** (not the
Workload name); Basic policies retain the normal workload's naming behavior. The
partition defaults to the scheduler configuration. See
[Annotations](workload.md#annotations) for the full key list.

If multiple layers set `slurmjob.slinky.slurm.net/job-name`, annotations are
applied **selected controller -> PodGroup -> Workload**, so the Workload wins.
In the example above, the PodGroup is named `training-job-workers` but the
Workload sets `slurmjob.slinky.slurm.net/job-name: training-job`, so Slurm
receives **`training-job`**. A PodGroup can override a `job-name` set on its
selected controller, but cannot override its Workload. For per-gang names, omit
`job-name` from the Workload and set it on each **PodGroup** instead.

A Workload may define several `podGroupTemplates`, each producing a runtime
PodGroup. Workload-level identifiers such as `job-name` then apply to **every**
PodGroup under that Workload. Each gang still submits a separate Slurm external
job (distinct job ID on the pods), but all share the same Slurm job **name** in
`squeue`. Use the Workload for **shared** parameters (partition, account, QOS,
time limit) and **PodGroup** for per-gang identifiers.

## Upgrading from Kubernetes 1.36 to 1.37

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

1. Restart the scheduler and controllers so both discover `v1beta1`:

   ```sh
   kubectl --namespace slurm rollout restart \
     deployment/slurm-bridge-scheduler deployment/slurm-bridge-controllers
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

See [Scaling and failure behavior](workload.md#scaling-and-failure-behavior) for
changing gang membership and releasing allocations.

<!-- Links -->

[workload-api]: https://kubernetes.io/docs/concepts/workloads/workload-api/
