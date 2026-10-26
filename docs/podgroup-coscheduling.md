# PodGroup coscheduling

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [PodGroup coscheduling](#podgroup-coscheduling)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Migrating from v1alpha1 PodGroups](#migrating-from-v1alpha1-podgroups)

<!-- mdformat-toc end -->

## Overview

> [!WARNING]
> Support for the **scheduler-plugins** PodGroup API
> `scheduling.x-k8s.io/v1alpha1` is deprecated in `slurm-bridge` and will be
> removed in an upcoming release. Use the [built-in PodGroup](podgroup.md) API
> for new workloads. Existing legacy workloads remain supported, but the
> [admission webhook](admission.md) warns on Pod creation and update when the
> `scheduling.x-k8s.io/pod-group` label is non-empty. This deprecation does not
> remove scheduling support.

To reject new legacy PodGroup Pods and updates that introduce legacy PodGroup
usage, set `admission.featureGates.Legacyv1alpha1PodGroups: false` (default:
`true`). Pods already using legacy PodGroups can still be updated, with a
deprecation warning, so metadata changes and finalizer removal remain possible.

For compatibility on clusters where the supported built-in PodGroup API is
unavailable, legacy workloads still require the
[PodGroup coscheduling CRD][podgroups-crd] plus the out-of-tree CoScheduling
controller:

```sh
helm install --repo https://scheduler-plugins.sigs.k8s.io scheduler-plugins scheduler-plugins \
  --namespace scheduler-plugins --create-namespace \
  --set 'plugins.enabled={CoScheduling}' --set 'scheduler.replicaCount=0'
```

Pods join the group via the label `scheduling.x-k8s.io/pod-group` (see
[`hack/examples/podgroup-coscheduling/`](https://github.com/SlinkyProject/slurm-bridge/tree/main/hack/examples/podgroup-coscheduling)).
Gang size is `spec.minMember` on the PodGroup object.

|                 | Built-in PodGroup                                                         | PodGroup coscheduling                 |
| --------------- | ------------------------------------------------------------------------- | ------------------------------------- |
| API version     | `scheduling.k8s.io/v1alpha2` (1.36) or `scheduling.k8s.io/v1beta1` (1.37) | `scheduling.x-k8s.io/v1alpha1`        |
| Install         | Feature gate + runtime config                                             | CRD + helm chart                      |
| Pod association | `spec.schedulingGroup.podGroupName`                                       | Label `scheduling.x-k8s.io/pod-group` |
| Gang field      | `spec.schedulingPolicy.gang.minCount`                                     | `spec.minMember`                      |

## Migrating from v1alpha1 PodGroups

The built-in and scheduler-plugins PodGroups are separate APIs with different
schemas. Changing only `apiVersion` is not sufficient.

1. Enable the built-in Workload and PodGroup APIs for your cluster as described
   in [Built-in PodGroup](podgroup.md): `scheduling.k8s.io/v1alpha2` on
   Kubernetes **1.36** or `scheduling.k8s.io/v1beta1` on **1.37**.
1. Update new Pod manifests or workload Pod templates to reference the built-in
   PodGroup through `spec.schedulingGroup.podGroupName`. Remove the
   `scheduling.x-k8s.io/pod-group` label; leaving it set continues to trigger
   the deprecation warning.
1. Let existing legacy workloads finish before removing their PodGroups. Keep
   the scheduler-plugins CRD and CoScheduling controller installed while any
   workloads still depend on them.

See [Workloads](workload.md) for shared scheduler configuration and
[scaling and failure behavior](workload.md#scaling-and-failure-behavior).

<!-- Links -->

[podgroups-crd]: https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/config/crd/bases/scheduling.x-k8s.io_podgroups.yaml
