# Admission

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Admission](#admission)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Design](#design)
    - [Sequence Diagram](#sequence-diagram)
  - [Workload annotation deprecation](#workload-annotation-deprecation)
  - [Unsupported scheduling requests](#unsupported-scheduling-requests)

<!-- mdformat-toc end -->

## Overview

[The Kubernetes documentation](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/)
defines admission controllers as:

> a piece of code that intercepts requests to the Kubernetes API server prior to
> persistence of the resource, but after the request is authenticated and
> authorized.

It also states that:

> Admission control mechanisms may be validating, mutating, or both. Mutating
> controllers may modify the data for the resource being modified; validating
> controllers may not.

The `slurm-bridge` admission controller is both mutating and validating. It
modifies pods within namespaces specified in `helm/slurm-bridge/values.yaml` to
use the `slurm-bridge` [scheduler] instead of the default Kubernetes scheduler,
and rejects unsupported scheduling and resource configurations.

## Design

Pods created in the specified namespaces with the default Kubernetes scheduler
have `.spec.schedulerName` changed to the slurm-bridge [scheduler]. Explicitly
selected custom schedulers are preserved. Pods selecting the bridge scheduler,
including those outside managed namespaces, receive its managed-node toleration
and, for unassigned pods that require registration, a scheduling gate. These
pods remain `SchedulingGated` until the controller registers their Slurm
allocation. The Helm webhooks exclude `kube-system` and the bridge's
installation namespace.

Managed namespaces are defined as a list of namespaces as configured in the
admission controller's `values.yaml` for `managedNamespaces[]`. Alternatively, a
`managedNamespaceSelector` can be used to select namespaces based on labels. If
`managedNamespaceSelector` is set, `managedNamespaces` will be ignored.

Pods selected for the slurm-bridge [scheduler] receive the
`slinky.slurm.net/managed-node` toleration during admission. If admission is
disabled or the pod's namespace is excluded from the webhook, set this
toleration explicitly to run on bridged nodes. The scheduler treats the pods it
receives as immutable and does not add tolerations.

Managed pods can request either native `cpu` or the CPU DRA extended resource
`deviceclass.resource.kubernetes.io/dra.cpu`, but cannot specify both. CPU DRA
must be requested explicitly; native `cpu` requests are not converted to DRA
requests by the admission controller.

CPU DRA requires `slurmjob.slinky.slurm.net/exclusive: "false"` on the
workload's [annotation source](workload.md#pod-grouping). Standalone pods that
omit it or enable exclusive allocation are rejected at admission. For
controller-owned and grouped pods, the scheduler checks the resolved annotations
before releasing the held Slurm job for allocation. Native CPU and GPU-only
requests still support exclusive allocation.

In-place resizing is not supported for managed pods. The admission controller
rejects requests to the `pods/resize` subresource so Kubernetes resources cannot
diverge from the corresponding Slurm allocation.

Pods cannot provide `spec.resourceClaims`. Request DRA devices through
DeviceClass extended resources instead.

The `slurmjob.slinky.slurm.net/constraints` annotation is combined with the
`slurm_bridge_gres_compatible` node feature that every bridge job requires. The
admission controller rejects Pod annotations containing constraint expressions
that Slurm's grammar does not allow to be combined with an additional feature,
such as a bare feature count (`rack1*2`) or an OR outside parentheses mixed with
parentheses or brackets (`(a&b)|(c&d)`), so the job fails at creation rather
than on every scheduling cycle. Annotations on owning workloads and PodGroups
are resolved later and checked before Slurm submission, not by the Pod admission
webhook. See [supported Slurm job annotations] for the supported forms.

Managed pods are also validated against the supported DRA DeviceClass set.
Unsupported DeviceClass resources in requests or limits are rejected for init
containers and regular containers. Operators can extend the supported set with
configured device profiles. See [Device resources] for the built-in classes,
profile configuration, and legacy device-plugin resources. Pods outside managed
namespaces that do not select the `slurm-bridge` scheduler are not subject to
this validation.

### Sequence Diagram

```mermaid
sequenceDiagram
  autonumber

  participant KAPI as Kubernetes API
  participant SBA as Slurm-Bridge Admission

  KAPI->>SBA: Admission request for Pod Create/Update
  opt Pod in a managed namespace or selecting the bridge scheduler
    note over SBA: Apply scheduler defaults, toleration, and creation gate
    note over SBA: Validate supported Pod configuration
  end
  SBA-->>KAPI: Admission response with patch or rejection
```

## Workload annotation deprecation

By default, a ValidatingAdmissionPolicy warns about top-level
`slurmjob.slinky.slurm.net/*` annotations on Jobs, JobSets, LeaderWorkerSets,
PodGroups, CompositePodGroups, and Workloads when they are created. Requests
remain allowed. Move annotations to Pod templates;
[Pod annotations take precedence](workload.md#resolution-rules) during the
transition. To reject these annotations instead, install or upgrade with
`--set admission.workloadAnnotationAction=Deny`. Set it to `Warn` (the default)
to allow requests with a warning. The policy only checks CREATE requests in both
modes; updates to existing workloads remain allowed.

The planned transition is warnings, then denial by default, then removal of
legacy annotation support. Release timing will depend on deprecation feedback.

The policy uses `admission.managedNamespaces` or
`admission.managedNamespaceSelector`, excluding `kube-system` and the bridge
installation namespace. Pod and Pod template annotations are allowed in both
modes. Disabling admission or clearing both namespace settings omits the policy.

## Unsupported scheduling requests

When admission is enabled, a ValidatingAdmissionPolicy checks pods on creation
after scheduler defaulting. It applies to pods selecting the bridge scheduler
explicitly or through a managed namespace. It excludes `kube-system` and the
bridge installation namespace.

On creation, the mutating webhook removes an explicit `spec.nodeName` from
bridge pods and returns a warning so Slurm can select the node. This applies in
both strict and warning modes; updates retain their existing node assignment.

The policy rejects these requests by default:

- A nonempty `spec.nodeSelector`, required or preferred node affinity, pod
  affinity or anti-affinity, and topology spread constraints. Use a Slurm
  partition or constraint for node placement.
- A volume source outside the supported set of `emptyDir`, `configMap`,
  `secret`, `projected`, `downwardAPI`, `image`, `nfs`, and
  `persistentVolumeClaim`. This includes `hostPath`, inline CSI, and generic
  ephemeral volumes. PVCs still go through Kubernetes VolumeBinding checks.

Set `--set admission.strictPodValidation=false` to admit these pods with a
warning. Slurm may place a pod without honoring the requested constraints. Pod
updates, including binding and registration, are outside this policy.

<!-- Links -->

[device resources]: workload.md#device-resources
[scheduler]: scheduler.md
[supported slurm job annotations]: workload.md#supported-slurm-job-annotations
