# QuickStart Guide

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [QuickStart Guide](#quickstart-guide)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Slurm Configuration](#slurm-configuration)
  - [Install `slurm-bridge`](#install-slurm-bridge)
    - [Pre-Requisites](#pre-requisites)
    - [Slurm Bridge](#slurm-bridge)
  - [Scheduling Workload](#scheduling-workload)
    - [Admission Controller](#admission-controller)
    - [Annotations](#annotations)
    - [JobSets](#jobsets)
    - [PodGroups](#podgroups)
    - [LeaderWorkerSet](#leaderworkerset)

<!-- mdformat-toc end -->

## Overview

This quickstart guide will help you get the slurm-bridge running and configured
with your existing Slurm cluster.

## Slurm Configuration

There are a set of assumptions that the slurm-bridge-scheduler must make. The
Slurm admin must satisfy those assumption in their Slurm cluster.

- There exists a set of hosts that have colocated [kubelet] and [slurmd]
  (installed on the same host, typically running as a systemd service).
- Slurm is configured with a partition with that only contains host with
  colocated [kubelet] and [slurmd].
  - The partition name must match the one configured in
    [values.yaml](../helm/slurm-bridge/values.yaml) used to deploy the
    slurm-bridge helm chart (default: "slurm-bridge").
  - Example `slurm.conf` snippet.
    ```conf
    # slurm.conf
    ...
    NodeSet=kubernetes Feature=kubernetes
    PartitionName=slurm-bridge Nodes=kubernetes
    ```
- In the event that the colocated node's Slurm NodeName does not match the
  Kubernetes Node name, you should patch the Kubernetes node with a label to
  allow slurm-bridge to map the colocated Kubernetes and Slurm node.
  ```bash
  kubectl patch node $KUBERNETES_NODENAME -p "{\"metadata\":{\"labels\":{\"slinky.slurm.net/slurm-nodename\":\"$SLURM_NODENAME\"}}}"
  ```
- Slurm has [Multi-Category Security][mcs] enabled for labels.
  - Example `slurm.conf` snippet.
  ```conf
  # slurm.conf
  ...
  MCSPlugin=mcs/label
  MCSParameters=ondemand,ondemandselect
  ```

## Install `slurm-bridge`

### Pre-Requisites

Install the pre-requisite helm charts.

```bash
helm repo update
helm install cert-manager jetstack/cert-manager \
	--namespace cert-manager --create-namespace --set crds.enabled=true
```

### Slurm Bridge

Create a secret for slurm-bridge to communicate with Slurm.

```sh
export SLURM_JWT=$(scontrol token username=slurm lifespan=infinite)
kubectl create namespace slurm-bridge
kubectl create secret generic slurm-bridge-jwt-token --namespace=slinky --from-literal="auth-token=$SLURM_JWT" --type=Opaque
```

Download values and install the `slurm-bridge` from OCI package:

```bash
curl -L https://raw.githubusercontent.com/SlinkyProject/slurm-bridge/refs/tags/v0.3.1/helm/slurm-bridge/values.yaml \
  -o values-bridge.yaml
helm install slurm-bridge oci://ghcr.io/slinkyproject/charts/slurm-bridge \
  --values=values-bridge.yaml --version=0.3.1 --namespace=slinky --create-namespace
```

**NOTE**: `slurm-bridge` must be able to communicate with Slurm REST API. By
default, it assumes a default Slurm chart installation and uses
http://slurm-restapi.slurm:6820.

You can check if your cluster deployed successfully with:

```sh
kubectl --namespace=slinky get pods
```

Your output should be similar to:

```sh
NAME                                        READY   STATUS    RESTARTS      AGE
slurm-bridge-admission-85f89cf884-8c9jt     1/1     Running   0             1m0s
slurm-bridge-controllers-757f64b875-bsfnf   1/1     Running   0             1m0s
slurm-bridge-scheduler-5484467f55-wtspk     1/1     Running   0             1m0s
```

## Scheduling Workload

Generally speaking, `slurm-bridge` translates one or more pods into a
representative Slurm workload, where Slurm does the underlying scheduling.
Certain optimizations can be made, depending on which resource is being
translated.

`slurm-bridge` has specific scheduling support for [JobSet](#jobsets) and
[PodGroup](#podgroups) resources and their pods. If your workload requires or
benefits from co-scheduled pod launch (e.g. MPI, multi-node), consider
representing your workload as a [JobSet](#jobsets) or [PodGroup](#podgroups).

### Admission Controller

`slurm-bridge` will only schedule pods who requests `slurm-bridge` as its
scheduler. The `slurm-bridge` admission controller can be configured to
automatically make `slurm-bridge` the scheduler for all pods created in the
configured namespaces.

Alternatively, a pod can specify `Pod.Spec.schedulerName=slurm-bridge-scheduler`
from any namespace.

### Annotations

Users can better inform or influence `slurm-bridge` how to represent their
Kubernetes workload within Slurm by adding
[annotations](../internal/wellknown/annotations.go) on the parent Object.

Example job to illustrate annotations:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: job-sleep-single
  namespace: slurm-bridge
  # slurm-bridge annotations on parent object
  annotations:
    slinky.slurm.net/job-name: job-sleep-single
    slinky.slurm.net/timelimit: "5"
    slinky.slurm.net/account: foo
spec:
  completions: 1
  parallelism: 1
  template:
    spec:
      containers:
        - name: sleep
          image: busybox:stable
          command: [sh, -c, sleep 30]
          resources:
            requests:
              cpu: '1'
              memory: 100Mi
            limits:
              cpu: '1'
              memory: 100Mi
      restartPolicy: Never
```

### JobSets

This section assumes [JobSets] is installed.

JobSet pods are scheduled on a per-pod basis. The JobSet controller is
responsible for managing the JobSet status and other Pod interactions once
marked as completed.

### PodGroups

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

### LeaderWorkerSet

This section assumes [LeaderWorkerSet] is installed.

LeaderWorkerSet groups will be co-scheduled so pods of each group will be
guaranteed to launch together.

**NOTE**: Topology-aware placement is not supported yet, so some features of
LeaderWorkerSet may not behave as expected.

<!-- Links -->

[jobsets]: https://jobset.sigs.k8s.io/
[kubelet]: https://kubernetes.io/docs/reference/command-line-tools-reference/kubelet
[leaderworkerset]: https://lws.sigs.k8s.io/
[mcs]: https://slurm.schedmd.com/mcs.html
[podgroups-crd]: https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/config/crd/bases/scheduling.x-k8s.io_podgroups.yaml
[slurmd]: https://slurm.schedmd.com/slurmd.html
