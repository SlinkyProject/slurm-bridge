# NVIDIA ComputeDomains and IMEX with DRA

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [NVIDIA ComputeDomains and IMEX with DRA](#nvidia-computedomains-and-imex-with-dra)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Prerequisites](#prerequisites)
  - [Install the NVIDIA DRA Driver](#install-the-nvidia-dra-driver)
  - [Configure Slurm](#configure-slurm)
  - [Configure the Device Profile](#configure-the-device-profile)
  - [Verify the Device Inventory](#verify-the-device-inventory)
  - [Describe NVLink Domains to Slurm](#describe-nvlink-domains-to-slurm)
  - [Create a ComputeDomain](#create-a-computedomain)
  - [Submit a Workload](#submit-a-workload)
  - [Verify an Allocation](#verify-an-allocation)
  - [Delete a ComputeDomain](#delete-a-computedomain)
  - [Limitations](#limitations)
  - [Troubleshooting](#troubleshooting)

<!-- mdformat-toc end -->

## Overview

A ComputeDomain lets Pods on several nodes share GPU memory over NVLink through
NVIDIA's Internode Memory Exchange (IMEX). The
[NVIDIA DRA Driver](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu)
starts an IMEX daemon on each node that joins a ComputeDomain and injects an
IMEX channel into the workload's containers.

Outside `slurm-bridge`, a Pod joins a ComputeDomain by referencing the
ResourceClaimTemplate that the driver generates for it. `slurm-bridge` creates
the ResourceClaim itself, from the Slurm allocation, and its admission
controller rejects Pods that set `spec.resourceClaims`. The generated template
only adds the ComputeDomain's identifier to a claim for the node's IMEX channel
device, so this guide delivers the identifier another way:

| Piece                  | Role                                                                                                                               |
| ---------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Device profile         | Registers each node's IMEX channel device (`channel-0`) as the Slurm GRES `imex:imex-channel:1`.                                   |
| ComputeDomain          | Created per workload or team, in the workload's namespace.                                                                         |
| DeviceClass            | One per ComputeDomain. Selects the channel device and carries the ComputeDomain's UID in `spec.config`.                            |
| Pod extended resource  | `deviceclass.resource.kubernetes.io/<class>: "1"` requests the channel. The bridge copies the class config into the ResourceClaim. |
| NVIDIA driver, kubelet | Prepare the claim: label the node, start the IMEX daemon Pod, and inject the channel once the daemon is ready.                     |

Slurm allocates the channel like any other indexed GRES, alongside the GPUs
described in [NVIDIA GPUs and MIG with DRA](nvidia-gpu-dra-mig.md). This guide
builds on that one and uses the same conventions: `kubectl` and `helm` against
the cluster where `slurm-bridge` is installed, Slurm in the `slurm` namespace,
and the controller Pod `slurm-controller-0`.

## Prerequisites

- Everything in the GPU guide's
  [Prerequisites](nvidia-gpu-dra-mig.md#prerequisites), including the
  `DRAExtendedResource` feature gate.
- GPU workers connected by NVLink, with the NVIDIA GPU driver's IMEX support
  installed. The driver identifies each node's NVLink domain as a clique, shown
  in the `nvidia.com/gpu.clique` node label when GPU Feature Discovery runs or
  when the DRA driver chart sets
  `kubeletPlugin.containers.computeDomains.gpuCliqueLabelEnabled: true`.
- External nodes. Hybrid nodes have not been validated; see
  [Limitations](#limitations).
- A node that `slurm-bridge` does not manage, for the driver's controller. The
  controller does not tolerate the `slinky.slurm.net/managed-node` taint by
  default.

## Install the NVIDIA DRA Driver

Use the values from
[Install the NVIDIA DRA Driver](nvidia-gpu-dra-mig.md#install-the-nvidia-dra-driver)
with ComputeDomains enabled:

```yaml
# nvidia-dra-values.yaml
gpuResourcesEnabledOverride: true
resources:
  gpus:
    enabled: true
  computeDomains:
    enabled: true
kubeletPlugin:
  nodeSelector:
    nvidia.com/gpu.present: "true"
  tolerations:
    - key: nvidia.com/gpu
      operator: Exists
      effect: NoSchedule
    - key: slinky.slurm.net/managed-node
      operator: Equal
      value: slurm-bridge-scheduler
      effect: NoExecute
```

Leave `resources.computeDomains.imex.mode` at its default, `driverManaged`.
Install or upgrade the chart. This guide was validated with version `0.5.1`:

```bash
helm upgrade --install dra-driver-nvidia-gpu \
  oci://registry.k8s.io/dra-driver-nvidia/charts/dra-driver-nvidia-gpu \
  --version 0.5.1 \
  --namespace dra-driver-nvidia-gpu --create-namespace \
  --values nvidia-dra-values.yaml --wait
```

The chart now also creates the `compute-domain-default-channel.nvidia.com` and
`compute-domain-daemon.nvidia.com` DeviceClasses, and each GPU worker publishes
two more devices under the `compute-domain.nvidia.com` driver:

```bash
export NODE=worker-1
kubectl get resourceslices \
  --field-selector spec.nodeName="$NODE",spec.driver=compute-domain.nvidia.com \
  -o jsonpath='{range .items[*].spec.devices[*]}{.name}{" "}{.attributes.type.string}{"\n"}{end}'
```

```text
daemon-0 daemon
channel-0 channel
```

`channel-0` is the device workloads request. `daemon-0` is used by the IMEX
daemon Pods and is not registered in Slurm.

## Configure Slurm

Add a GRES name for the channel to Slurm's `GresTypes`. This guide uses `imex`:

```conf
GresTypes=gpu,nic,imex
```

Slurm omits a GRES from the node inventory when its name is not listed.

## Configure the Device Profile

Add a device profile for the channel device. Its selector must exactly match the
selector of the chart's `compute-domain-default-channel.nvidia.com` DeviceClass.
Setting `sharedConfig.deviceProfiles` replaces the built-in list, so repeat the
built-in profiles you still need:

```yaml
# slurm-bridge values.yaml
sharedConfig:
  deviceProfiles:
    - name: cpu
      driver: dra.cpu
      selector: device.driver == "dra.cpu"
      backend:
        type: core-bitmap
    - name: gpu.nvidia.com
      driver: gpu.nvidia.com
      selector: >-
        device.driver == 'gpu.nvidia.com' &&
        device.attributes['gpu.nvidia.com'].type == 'gpu'
      backend:
        type: indexed-gres
        gresName: gpu
    - name: imex-channel
      driver: compute-domain.nvidia.com
      selector: >-
        device.driver == 'compute-domain.nvidia.com' &&
        device.attributes['compute-domain.nvidia.com'].type == 'channel' &&
        device.attributes['compute-domain.nvidia.com'].id == 0
      backend:
        type: indexed-gres
        gresName: imex
    - name: dranet-rdma
      driver: dra.net
      selector: >-
        device.driver == 'dra.net' &&
        has(device.attributes['dra.net'].pciAddress) &&
        has(device.attributes['dra.net'].rdma) &&
        device.attributes['dra.net'].rdma == true
      backend:
        type: indexed-gres
        gresName: nic
```

Apply the values with `helm upgrade slurm-bridge`.

## Verify the Device Inventory

Confirm that the bridge registered the channel with Slurm:

```bash
kubectl exec -n slurm slurm-controller-0 -- scontrol show node "$NODE" | \
  grep -E 'Gres=|Extra='
```

```text
   Gres=gpu:gpu.nvidia.com:4,imex:imex-channel:1
   Extra=slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.nvidia.com":{"firstIndex":0,"devices":["/dra/gpu.nvidia.com/worker-1/gpu-0","/dra/gpu.nvidia.com/worker-1/gpu-1","/dra/gpu.nvidia.com/worker-1/gpu-2","/dra/gpu.nvidia.com/worker-1/gpu-3"]},"imex-channel":{"firstIndex":0,"devices":["/dra/compute-domain.nvidia.com/worker-1/channel-0"]}}}
```

Each node has one `imex-channel` GRES, so Slurm places at most one ComputeDomain
Pod on a node.

## Describe NVLink Domains to Slurm

IMEX only connects nodes in the same NVLink clique, and Slurm does not read the
`nvidia.com/gpu.clique` label. Give Slurm the grouping so that it can keep a
workload inside one clique. For external nodes, add a Slurm feature per clique
with the `feature.slinky.slurm.net/spec` node annotation:

```bash
kubectl get nodes -L nvidia.com/gpu.clique
kubectl annotate node worker-0 worker-1 feature.slinky.slurm.net/spec=nvlink-ab
kubectl annotate node worker-2 worker-3 feature.slinky.slurm.net/spec=nvlink-cd
```

Workloads then select a clique with the `slurmjob.slinky.slurm.net/constraints`
annotation. A partition per clique, selected with
`slurmjob.slinky.slurm.net/partition`, or a Slurm [topology](config.md#topology)
that mirrors the NVLink domains achieves the same result.

## Create a ComputeDomain

Create the ComputeDomain in the namespace of the workload that uses it:

```yaml
apiVersion: resource.nvidia.com/v1beta1
kind: ComputeDomain
metadata:
  name: imex-demo
  namespace: slurm-bridge
spec:
  numNodes: 0
  channel:
    allocationMode: Single
    resourceClaimTemplate:
      name: imex-demo-channel
```

`resourceClaimTemplate.name` is required. The driver creates that
ResourceClaimTemplate, but bridge workloads do not reference it.

Then create a DeviceClass for the ComputeDomain. A DeviceClass is
cluster-scoped, so a cluster administrator creates it. Its selector is the
device profile's selector, and its `domainID` is the UID of the ComputeDomain:

```bash
kubectl get computedomain -n slurm-bridge imex-demo -o jsonpath='{.metadata.uid}'
```

```text
2f924d1c-338f-44b8-b494-0a5a8b5e39ec
```

```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: cd-imex-demo
spec:
  selectors:
    - cel:
        expression: >-
          device.driver == 'compute-domain.nvidia.com' &&
          device.attributes['compute-domain.nvidia.com'].type == 'channel' &&
          device.attributes['compute-domain.nvidia.com'].id == 0
  config:
    - opaque:
        driver: compute-domain.nvidia.com
        parameters:
          apiVersion: resource.nvidia.com/v1beta1
          kind: ComputeDomainChannelConfig
          domainID: 2f924d1c-338f-44b8-b494-0a5a8b5e39ec
          allocationMode: Single
```

Every ComputeDomain's DeviceClass uses the same selector and resolves to the
same `imex-channel` profile; only the name and the `domainID` differ.

The driver only prepares a channel for a Pod in the ComputeDomain's own
namespace. Pods in other namespaces can request the DeviceClass, but they do not
join the ComputeDomain; see [Troubleshooting](#troubleshooting).

## Submit a Workload

Request the ComputeDomain's DeviceClass with a quantity of `1`, next to the GPU
request, and constrain the workload to one NVLink clique:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: imex-demo
  namespace: slurm-bridge
  # Slurm job annotations on the Job, not spec.template.metadata
  annotations:
    slurmjob.slinky.slurm.net/constraints: nvlink-ab
spec:
  completions: 2
  parallelism: 2
  template:
    spec:
      schedulerName: slurm-bridge-scheduler
      restartPolicy: Never
      containers:
        - name: worker
          image: busybox:1.36
          # Fails the Pod when the IMEX channel was not injected.
          command:
            - sh
            - -c
            - ls -l /dev/nvidia-caps-imex-channels && sleep 300
          resources:
            requests:
              cpu: "1"
              memory: 256Mi
              deviceclass.resource.kubernetes.io/gpu.nvidia.com: "1"
              deviceclass.resource.kubernetes.io/cd-imex-demo: "1"
            limits:
              cpu: "1"
              memory: 256Mi
              deviceclass.resource.kubernetes.io/gpu.nvidia.com: "1"
              deviceclass.resource.kubernetes.io/cd-imex-demo: "1"
```

Do not add `spec.resourceClaims`. Each Pod of this Job is its own Slurm job, on
its own node. To allocate the Pods of a multi-node workload together, group them
as described in [Pod grouping](workload.md#pod-grouping).

When the first Pod of a ComputeDomain is bound to a node, the driver labels the
node with `resource.nvidia.com/computeDomain` and starts an IMEX daemon Pod
there, in the driver's namespace. The workload Pod stays in `ContainerCreating`
until that daemon is ready. When the Pod is deleted, the driver removes the
label and the daemon Pod.

## Verify an Allocation

Select one of the Job's Pods by label. Its generated ResourceClaim holds the
channel, the GPU, and the configuration copied from the DeviceClass:

```bash
export POD=$(kubectl get pods -n slurm-bridge \
  -l batch.kubernetes.io/job-name=imex-demo \
  -o jsonpath='{.items[0].metadata.name}')
export CLAIM=$(kubectl get pod -n slurm-bridge "$POD" \
  -o jsonpath='{.status.extendedResourceClaimStatus.resourceClaimName}')
kubectl get resourceclaim -n slurm-bridge "$CLAIM" \
  -o jsonpath='{range .status.allocation.devices.results[*]}{.request}{" "}{.driver}{" "}{.device}{"\n"}{end}'
kubectl get resourceclaim -n slurm-bridge "$CLAIM" \
  -o jsonpath='{.status.allocation.devices.config}{"\n"}'
```

```text
imex compute-domain.nvidia.com channel-0
gpu gpu.nvidia.com gpu-0
[{"opaque":{"driver":"compute-domain.nvidia.com","parameters":{"allocationMode":"Single","apiVersion":"resource.nvidia.com/v1beta1","domainID":"2f924d1c-338f-44b8-b494-0a5a8b5e39ec","kind":"ComputeDomainChannelConfig"}},"requests":["imex"],"source":"FromClass"}]
```

The ComputeDomain lists the nodes that joined it and their clique, and the
container's check lists the injected IMEX channel:

```bash
kubectl get computedomain -n slurm-bridge imex-demo -o jsonpath='{.status}{"\n"}'
kubectl logs -n slurm-bridge "$POD"
```

```text
{"nodes":[{"cliqueID":"00000000-0000-0000-0000-0000000000ab.0","index":0,"ipAddress":"10.244.2.16","name":"worker-1","status":"Ready"},{"cliqueID":"00000000-0000-0000-0000-0000000000ab.0","index":1,"ipAddress":"10.244.4.16","name":"worker-0","status":"Ready"}],"status":"Ready"}
total 0
crw-rw-rw-    1 root     root      235,   0 Nov 10 14:13 channel0
```

The Slurm job holds the matching GRES:

```bash
kubectl exec -n slurm slurm-controller-0 -- \
  sh -c 'for j in $(squeue -h -o %i); do scontrol show job -d $j; done' | \
  grep -E 'AdminComment|GRES=|Features='
```

```text
     Nodes=worker-1 CPU_IDs=0-13 Mem=256 GRES=gpu:gpu.nvidia.com:4(IDX:0-3),imex:imex-channel:1(IDX:0)
   Features=slurm_bridge_gres_compatible&nvlink-ab DelayBoot=00:00:00
   AdminComment={"pods":"slurm-bridge/imex-demo-85rp9/5968dd20-d0ef-4a1f-a99f-a8003798a729"}
```

## Delete a ComputeDomain

Delete the workloads first, then the ComputeDomain and its DeviceClass:

```bash
kubectl delete job -n slurm-bridge imex-demo
kubectl delete computedomain -n slurm-bridge imex-demo
kubectl delete deviceclass cd-imex-demo
```

A recreated ComputeDomain has a new UID. Update the `domainID` in its
DeviceClass, or Pods that request the class fail to start.

## Limitations

- One ComputeDomain Pod per node. The driver publishes a single channel device
  per node, so a second Pod that requests any ComputeDomain waits in Slurm with
  reason `Resources`, even with `slurmjob.slinky.slurm.net/exclusive: "false"`.
- The DeviceClass is not tied to the ComputeDomain's lifecycle. Create, update,
  and delete it together with the ComputeDomain.
- Slurm does not know which nodes share an NVLink clique. Without a constraint,
  partition, or topology, it may place a workload across cliques, where the Pods
  start but cannot exchange memory.
- IMEX daemon Pods run outside Slurm. The default Kubernetes scheduler places
  them, they tolerate every taint, and their CPU and memory are not accounted to
  the Slurm job.
- Only driver-managed IMEX is covered. Host-managed IMEX
  (`resources.computeDomains.imex.mode: hostManaged`) and Slurm's
  `switch/nvidia_imex` plugin have not been validated with `slurm-bridge`.
- Hybrid nodes have not been validated. Their `gres.conf` needs an entry that
  matches the profile, such as
  `Name=imex Type=imex-channel File=/dev/null Count=1`; see
  [Hybrid Nodes](config.md#hybrid-nodes).
- Validation used emulated NVLink hardware, which exercises scheduling, claim
  preparation, and the IMEX daemon lifecycle, but not memory exchange between
  GPUs.

## Troubleshooting

| Symptom                                                                                                                                             | Checks                                                                                                                                                                                |
| --------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `scontrol show node` has no `imex:` GRES                                                                                                            | Add `imex` to Slurm's `GresTypes`, check that the `imex-channel` profile is configured, and check that the node publishes `channel-0` in a `compute-domain.nvidia.com` ResourceSlice. |
| Admission rejects the Pod: `selector does not match a supported device profile`                                                                     | Make the DeviceClass selector identical to the `imex-channel` profile selector, including whitespace after YAML folding.                                                              |
| Pod stays `ContainerCreating` with `FailedPrepareDynamicResources`: `the ResourceClaim's namespace is different than the ComputeDomain's namespace` | The Pod is not in the ComputeDomain's namespace. Delete it; it holds its Slurm allocation until then.                                                                                 |
| Pod stays `ContainerCreating` with `FailedPrepareDynamicResources`: `ComputeDomain not found: <uid>`                                                | The `domainID` in the DeviceClass is not the UID of an existing ComputeDomain. Update it after recreating the ComputeDomain, then recreate the Pod.                                   |
| Pod stays `ContainerCreating` with `FailedPrepareDynamicResources`: `current node not ready in ComputeDomain`                                       | The IMEX daemon Pod on the node is not ready. Check the `computedomain-daemon-` Pods in the driver's namespace.                                                                       |
| Pod stays `Pending` and the Slurm job reason is `Resources`                                                                                         | Every eligible node already runs a ComputeDomain Pod, or the constraint leaves too few nodes.                                                                                         |
| Pod stays `SchedulingGated` with a `SlurmRegistrationRejected` event: `Invalid feature specification`                                               | No Slurm node has the requested feature. Check the `feature.slinky.slurm.net/spec` annotations and `AvailableFeatures` in `scontrol show node`.                                       |
