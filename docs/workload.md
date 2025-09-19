# Workloads

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Workloads](#workloads)
  - [Using the `slurm-bridge` Scheduler](#using-the-slurm-bridge-scheduler)
  - [Annotations](#annotations)
  - [JobSets](#jobsets)
  - [PodGroups](#podgroups)
  - [LeaderWorkerSet](#leaderworkerset)

<!-- mdformat-toc end -->

In Slurm, all workloads are represented by jobs. In `slurm-bridge`, however,
there are a number of forms that workloads can take. While workloads can still
be submitted as a Slurm job, `slurm-bridge` also enables users to submit
workloads through Kubernetes. Most workloads that can be submitted to
`slurm-bridge` from within Kubernetes are represented by an existing Kubernetes
batch workload primitive.

At this time, `slurm-bridge` has scheduling support for [Jobs],
[JobSets](#jobsets), [Pods], [PodGroups](#podgroups), and [LeaderWorkerSets]. If
your workload requires or benefits from co-scheduled pod launch (e.g. MPI,
multi-node), consider representing your workload as a [PodGroup](#podgroups) or
[LeaderWorkerSets](#leaderworkersets).

## Using the `slurm-bridge` Scheduler

`slurm-bridge` uses an
[admission controller](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/)
to control which resources are scheduled using the `slurm-bridge-scheduler`. The
`slurm-bridge-scheduler` is designed as a non-primary scheduler and is not
intended to replace the default
[kube-scheduler](https://kubernetes.io/docs/concepts/architecture/#kube-scheduler).
The `slurm-bridge` admission controller only schedules pods that request
`slurm-bridge` as their scheduler or are in a configured namespace. By default,
the `slurm-bridge` admission controller is configured to automatically use
`slurm-bridge` as the scheduler for all pods in the configured namespaces.

Alternatively, a pod can specify `Pod.Spec.schedulerName=slurm-bridge-scheduler`
from any namespace to indicate that it should be scheduler using the
`slurm-bridge-scheduler`.

You can learn more about the `slurm-bridge` admission controller
[here](../../concepts/admission).

## Annotations

Users can better inform or influence `slurm-bridge` how to represent their
Kubernetes workload within Slurm by adding
[annotations](../internal/wellknown/annotations.go) on the parent Object.

Example "pause" bare pod to illustrate annotations:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pause
  # `slurm-bridge` annotations on parent object
  annotations:
    slinky.slurm.net/timelimit: "5"
    slinky.slurm.net/account: foo
spec:
  schedulerName: slurm-bridge-scheduler
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.6
      resources:
        limits:
          cpu: "1"
          memory: 100Mi
```

Example "pause" deployment to illustrate annotations:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pause
  # `slurm-bridge` annotations on parent object
  annotations:
    slinky.slurm.net/timelimit: "5"
    slinky.slurm.net/account: foo
spec:
  replicas: 2
  selector:
    matchLabels:
      app: pause
  template:
    metadata:
      labels:
        app: pause
    spec:
      schedulerName: slurm-bridge-scheduler
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.6
          resources:
            limits:
              cpu: "1"
              memory: 100Mi
```

## JobSets

This section assumes [JobSets] is installed.

JobSet pods are scheduled on a per-pod basis. The JobSet controller is
responsible for managing the JobSet status and other Pod interactions once
marked as completed.

## PodGroups

This section assumes [PodGroups CRD][podgroups-crd] and the out-of-tree
kube-scheduler controller for CoScheduling is installed.

```sh
helm install --repo https://scheduler-plugins.sigs.k8s.io scheduler-plugins scheduler-plugins \
			--namespace scheduler-plugins --create-namespace \
			--set 'plugins.enabled={CoScheduling}' --set 'scheduler.replicaCount=0'
```

Pods contained within a PodGroup will be co-scheduled and launched together. The
PodGroup controller is responsible for managing the PodGroup status and other
Pod interactions once marked as completed.

## LeaderWorkerSet

This section assumes [LeaderWorkerSet] is installed.

LeaderWorkerSet groups will be co-scheduled so pods of each group will be
guaranteed to launch together.

> [!NOTE]
> Topology-aware placement is not supported yet, so some features of
> LeaderWorkerSet may not behave as expected.

<!-- Links -->

[jobs]: https://kubernetes.io/docs/concepts/workloads/controllers/job/
[jobsets]: https://jobset.sigs.k8s.io/
[leaderworkerset]: https://lws.sigs.k8s.io/
[leaderworkersets]: https://lws.sigs.k8s.io/
[podgroups-crd]: https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/config/crd/bases/scheduling.x-k8s.io_podgroups.yaml
[pods]: https://kubernetes.io/docs/concepts/workloads/pods/
