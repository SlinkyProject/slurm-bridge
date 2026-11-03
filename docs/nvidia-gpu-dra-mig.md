# NVIDIA GPUs and MIG with DRA

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [NVIDIA GPUs and MIG with DRA](#nvidia-gpus-and-mig-with-dra)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Prerequisites](#prerequisites)
  - [Install the NVIDIA DRA Driver](#install-the-nvidia-dra-driver)
  - [Verify the Device Inventory](#verify-the-device-inventory)
  - [Request Full GPUs](#request-full-gpus)
  - [Request MIG Devices](#request-mig-devices)
    - [Partition the GPUs](#partition-the-gpus)
    - [Configure MIG DeviceClasses and Profiles](#configure-mig-deviceclasses-and-profiles)
    - [Submit a MIG Workload](#submit-a-mig-workload)
    - [Change the MIG Layout](#change-the-mig-layout)
  - [Verify an Allocation](#verify-an-allocation)
  - [Sharing a GPU Node](#sharing-a-gpu-node)
  - [Limitations](#limitations)
  - [Troubleshooting](#troubleshooting)

<!-- mdformat-toc end -->

## Overview

`slurm-bridge` can schedule Pods that request NVIDIA GPUs through
[Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
(DRA) using the
[NVIDIA DRA Driver](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu).
Slurm chooses the devices: the bridge controller registers each DRA device as an
indexed Slurm GRES, Slurm allocates GRES indices to the representative job, and
the bridge translates that allocation into a ResourceClaim for the Pod. The
kubelet then prepares exactly the devices Slurm selected.

The NVIDIA driver publishes full GPUs and MIG devices. Pods request them through
DeviceClass extended resources:

| DeviceClass                     | Device                                 | Extended resource                                   | Slurm GRES               |
| ------------------------------- | -------------------------------------- | --------------------------------------------------- | ------------------------ |
| `gpu.nvidia.com` (driver chart) | Full GPU (`type == 'gpu'`)             | `deviceclass.resource.kubernetes.io/gpu.nvidia.com` | `gpu:gpu.nvidia.com:<n>` |
| `mig-1g.10gb` (example)         | MIG device of one size, here `1g.10gb` | `deviceclass.resource.kubernetes.io/mig-1g.10gb`    | `gpu:mig-1g.10gb:<n>`    |

Full GPUs are supported by the built-in `gpu.nvidia.com` device profile. MIG
devices require a DeviceClass and a device profile for each MIG size, described
in
[Configure MIG DeviceClasses and Profiles](#configure-mig-deviceclasses-and-profiles).

This guide uses `kubectl` and `helm` against the cluster where `slurm-bridge` is
installed, and assumes the slurm-operator defaults: Slurm in the `slurm`
namespace, with the controller Pod `slurm-controller-0`.

## Prerequisites

- `slurm-bridge` installed as described in the
  [QuickStart Guide](quickstart.md), with the GPU workers configured as
  [external or hybrid nodes](config.md#nodes).
- The NVIDIA GPU driver installed on each GPU worker, and a container runtime
  with [CDI](https://github.com/cncf-tags/container-device-interface) enabled.
  The NVIDIA DRA driver prepares devices through CDI specifications.
- The `DRAExtendedResource` feature gate enabled on the API server, scheduler,
  controller manager, and kubelet. `slurm-bridge` links each Pod to its
  ResourceClaim through `status.extendedResourceClaimStatus`. Without the gate,
  the API server drops that field and the kubelet rejects the Pod.
- `gpu` listed in Slurm's `GresTypes`, for example `GresTypes=gpu,nic`. Slurm
  omits a GRES from the node inventory when its type is not listed. See
  [Supported DRA DeviceClasses](workload.md#supported-dra-deviceclasses).
- The NVIDIA device plugin (`nvidia.com/gpu`) disabled on GPU workers managed by
  the DRA driver. The two cannot manage the same GPUs.
- For hybrid nodes, a `gres.conf` whose GPU entries match the DRA inventory; see
  [Hybrid Nodes](config.md#hybrid-nodes). List the devices in the string order
  of their DRA device names, which differs from numeric order on nodes with more
  than ten GPUs: `gpu-10` and `gpu-11` come before `gpu-2`. If `AutoDetect` is
  used, check that it produces the same order.

## Install the NVIDIA DRA Driver

`slurm-bridge` applies the `slinky.slurm.net/managed-node` `NoExecute` taint to
the nodes it manages. The driver's kubelet plugin must tolerate that taint, or
Kubernetes evicts it from those nodes and their GPUs disappear from the
ResourceSlices. Keep the chart's default `nvidia.com/gpu` toleration, because
setting `kubeletPlugin.tolerations` replaces the list:

```yaml
# nvidia-dra-values.yaml
# Required to publish GPUs; see the chart's validation message.
gpuResourcesEnabledOverride: true
resources:
  gpus:
    enabled: true
  computeDomains:
    enabled: false
kubeletPlugin:
  # Select GPU workers. The chart default selects nodes through
  # Node Feature Discovery PCI labels.
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

Adjust the node selection for your cluster. When Node Feature Discovery labels
are present, the chart's default `kubeletPlugin.affinity` is sufficient, and the
`nodeSelector` above can be omitted. Set `nvidiaDriverRoot` when the GPU driver
is not installed at `/` on the host, for example `/run/nvidia/driver` when the
driver runs in the NVIDIA driver container.

Install the chart. This guide was validated with version `0.5.1`:

```bash
helm install dra-driver-nvidia-gpu \
  oci://registry.k8s.io/dra-driver-nvidia/charts/dra-driver-nvidia-gpu \
  --version 0.5.1 \
  --namespace dra-driver-nvidia-gpu --create-namespace \
  --values nvidia-dra-values.yaml --wait
```

The chart creates the `gpu.nvidia.com`, `mig.nvidia.com`, and
`vfio.gpu.nvidia.com` DeviceClasses. `vfio.gpu.nvidia.com` has no matching
device profile, so the admission controller rejects Pods that request it.

## Verify the Device Inventory

List the devices published for a GPU worker:

```bash
export NODE=gpu-worker-1
kubectl get resourceslices \
  --field-selector spec.nodeName="$NODE",spec.driver=gpu.nvidia.com \
  -o jsonpath='{range .items[*].spec.devices[*]}{.name}{" "}{.attributes.type.string}{" "}{.attributes.uuid.string}{"\n"}{end}'
```

```text
gpu-0 gpu GPU-b74fc06c-2972-5ff3-18ca-1e312a74f522
```

Then confirm that the bridge registered the devices with Slurm:

```bash
kubectl exec -n slurm slurm-controller-0 -- scontrol show node "$NODE" | \
  grep -E 'Gres=|Extra='
```

```text
   Gres=gpu:gpu.nvidia.com:1
   Extra=slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.nvidia.com":{"firstIndex":0,"devices":["/dra/gpu.nvidia.com/gpu-worker-1/gpu-0"]}}}
```

`Extra` holds the bridge's mapping between Slurm GRES indices and DRA devices.
Profiles that share a GRES name, such as `gpu`, share one index space, so a
profile's `firstIndex` is not always `0`. Within a profile, devices are indexed
from `firstIndex` in the string order of their names. For example, on a node
whose only `gpu` profile has twelve devices named `gpu-0` to `gpu-11`, the order
is `gpu-0`, `gpu-1`, `gpu-10`, `gpu-11`, `gpu-2`, and so on, so `gpu-10` has
index `2` and `gpu-2` has index `4`. Read the actual mapping from `Extra`.

## Request Full GPUs

Request GPUs with the DeviceClass extended resource. The quantity is the number
of GPUs, and requests must equal limits:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cuda-vectoradd
  namespace: slurm-bridge
spec:
  schedulerName: slurm-bridge-scheduler
  restartPolicy: Never
  containers:
    - name: cuda
      image: nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0
      resources:
        requests:
          cpu: "1"
          memory: 256Mi
          deviceclass.resource.kubernetes.io/gpu.nvidia.com: "1"
        limits:
          cpu: "1"
          memory: 256Mi
          deviceclass.resource.kubernetes.io/gpu.nvidia.com: "1"
```

Do not add `spec.resourceClaims`; the admission controller rejects Pods that set
it, because the bridge creates the ResourceClaim from the Slurm allocation.

## Request MIG Devices

### Partition the GPUs

`slurm-bridge` and the NVIDIA DRA driver consume MIG devices that already exist;
neither creates nor resizes them. Create the MIG layout on each GPU worker with
your usual tooling, such as the
[NVIDIA MIG Manager](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-operator-mig.html)
or `nvidia-smi`. For example, to split an H100 80GB into one `3g.40gb`, one
`2g.20gb`, and two `1g.10gb` devices:

```bash
sudo nvidia-smi -i 0 -mig 1
sudo nvidia-smi mig -i 0 -cgi 3g.40gb,2g.20gb,1g.10gb,1g.10gb -C
nvidia-smi -L
```

```text
GPU 0: NVIDIA H100 80GB HBM3 (UUID: GPU-2f44c076-5031-59e2-f578-ea4e457c775c)
  MIG 3g.40gb     Device  0: (UUID: MIG-9b65b988-b1b6-56ba-8a64-03ce617e643b)
  MIG 2g.20gb     Device  1: (UUID: MIG-a8393641-5319-5624-be92-a5a8d2db0fa4)
  MIG 1g.10gb     Device  2: (UUID: MIG-81e414a3-4d14-5182-afc4-e6e95f3dfd78)
  MIG 1g.10gb     Device  3: (UUID: MIG-03bc3e47-b4c2-5477-a28d-752c2068077c)
```

Restart the driver's kubelet plugin on that node so it publishes the new
devices:

```bash
kubectl delete pod --namespace dra-driver-nvidia-gpu \
  --selector dra-driver-nvidia-gpu-component=kubelet-plugin \
  --field-selector spec.nodeName="$NODE"
```

A GPU in MIG mode is published only as its MIG devices, each with `type: mig`
and a `profile` attribute such as `1g.10gb`:

```bash
kubectl get resourceslices \
  --field-selector spec.nodeName="$NODE",spec.driver=gpu.nvidia.com \
  -o jsonpath='{range .items[*].spec.devices[*]}{.name}{" "}{.attributes.type.string}{" "}{.attributes.profile.string}{"\n"}{end}'
```

```text
gpu-0-mig-2g20gb-14-0 mig 2g.20gb
gpu-0-mig-1g10gb-19-2 mig 1g.10gb
gpu-0-mig-1g10gb-19-3 mig 1g.10gb
gpu-0-mig-3g40gb-9-4 mig 3g.40gb
```

### Configure MIG DeviceClasses and Profiles

The built-in NVIDIA profile only selects devices with `type == 'gpu'`, so MIG
devices are ignored until a profile selects them. Create a DeviceClass for each
MIG size that workloads request, selecting the size through the device's
`profile` attribute:

```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: mig-1g.10gb
spec:
  selectors:
    - cel:
        expression: >-
          device.driver == 'gpu.nvidia.com' &&
          device.attributes['gpu.nvidia.com'].type == 'mig' &&
          device.attributes['gpu.nvidia.com'].profile == '1g.10gb'
```

Repeat it for the other sizes in the layout, here `mig-2g.20gb` and
`mig-3g.40gb`, changing the name and the `profile` value. Pods request a size
with the DeviceClass extended resource, such as
`deviceclass.resource.kubernetes.io/mig-1g.10gb`.

Then add one device profile per DeviceClass. Each profile selector must exactly
match its DeviceClass selector, and the profile name becomes the Slurm GRES
type. Setting `sharedConfig.deviceProfiles` replaces the built-in list, so
repeat the built-in profiles you still need:

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
    - name: mig-1g.10gb
      driver: gpu.nvidia.com
      selector: >-
        device.driver == 'gpu.nvidia.com' &&
        device.attributes['gpu.nvidia.com'].type == 'mig' &&
        device.attributes['gpu.nvidia.com'].profile == '1g.10gb'
      backend:
        type: indexed-gres
        gresName: gpu
    - name: mig-2g.20gb
      driver: gpu.nvidia.com
      selector: >-
        device.driver == 'gpu.nvidia.com' &&
        device.attributes['gpu.nvidia.com'].type == 'mig' &&
        device.attributes['gpu.nvidia.com'].profile == '2g.20gb'
      backend:
        type: indexed-gres
        gresName: gpu
    - name: mig-3g.40gb
      driver: gpu.nvidia.com
      selector: >-
        device.driver == 'gpu.nvidia.com' &&
        device.attributes['gpu.nvidia.com'].type == 'mig' &&
        device.attributes['gpu.nvidia.com'].profile == '3g.40gb'
      backend:
        type: indexed-gres
        gresName: gpu
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

The selectors are disjoint, as required for profiles of the same driver. MIG
devices whose size has no profile are not registered in Slurm. Apply the values
with `helm upgrade slurm-bridge`, then confirm the Slurm inventory:

```bash
kubectl exec -n slurm slurm-controller-0 -- scontrol show node "$NODE" | \
  grep -E 'Gres=|Extra='
```

```text
   Gres=gpu:mig-1g.10gb:2,gpu:mig-2g.20gb:1,gpu:mig-3g.40gb:1
   Extra=slurm-bridge.dra-gres-map={"v":1,"profiles":{"mig-1g.10gb":{"firstIndex":0,"devices":["/dra/gpu.nvidia.com/gpu-worker-1/gpu-0-mig-1g10gb-19-2","/dra/gpu.nvidia.com/gpu-worker-1/gpu-0-mig-1g10gb-19-3"]},"mig-2g.20gb":{"firstIndex":2,"devices":["/dra/gpu.nvidia.com/gpu-worker-1/gpu-0-mig-2g20gb-14-0"]},"mig-3g.40gb":{"firstIndex":3,"devices":["/dra/gpu.nvidia.com/gpu-worker-1/gpu-0-mig-3g40gb-9-4"]}}}
```

To schedule any free MIG device regardless of size instead, configure a single
profile matching the driver chart's `mig.nvidia.com` DeviceClass
(`device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'mig'`).
It overlaps with the per-size profiles, so use one approach or the other.

### Submit a MIG Workload

Request MIG devices of one size with its DeviceClass extended resource. The
quantity is the number of MIG devices:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: cuda-mig
  namespace: slurm-bridge
  # Slurm job annotations on the Job, not spec.template.metadata
  annotations:
    slurmjob.slinky.slurm.net/exclusive: "false"
spec:
  completions: 2
  parallelism: 2
  template:
    spec:
      schedulerName: slurm-bridge-scheduler
      restartPolicy: Never
      containers:
        - name: cuda
          image: nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0
          resources:
            requests:
              cpu: "1"
              memory: 256Mi
              deviceclass.resource.kubernetes.io/mig-1g.10gb: "1"
            limits:
              cpu: "1"
              memory: 256Mi
              deviceclass.resource.kubernetes.io/mig-1g.10gb: "1"
```

Each Pod receives its own `1g.10gb` device, and the two Pods share the GPU node;
see [Sharing a GPU Node](#sharing-a-gpu-node). When all `1g.10gb` devices are
allocated, further `mig-1g.10gb` jobs wait in Slurm with reason `Resources`, and
their Pods remain `Pending` until a device of that size is released, even if
devices of other sizes are free.

### Change the MIG Layout

The bridge rewrites the Slurm GRES inventory when the ResourceSlices change.
Before changing a node's MIG layout:

1. Drain the Slurm node and wait for its bridge workloads to finish, so that no
   ResourceClaim holds a MIG device on the node.
1. Reconfigure MIG on the GPUs.
1. Restart the driver's kubelet plugin on the node, as shown in
   [Partition the GPUs](#partition-the-gpus).
1. Confirm the new `Gres=` inventory with `scontrol show node`, then resume the
   Slurm node.

Switching a GPU between full-GPU and MIG mode follows the same procedure. The
GRES moves between the `gpu.nvidia.com` type and the MIG size types. A new MIG
size also needs its DeviceClass and device profile before workloads can request
it.

## Verify an Allocation

The bridge records the generated ResourceClaim in the Pod status:

```bash
export POD=cuda-mig-k9xnw
export CLAIM=$(kubectl get pod -n slurm-bridge "$POD" \
  -o jsonpath='{.status.extendedResourceClaimStatus.resourceClaimName}')
kubectl get resourceclaim -n slurm-bridge "$CLAIM" \
  -o jsonpath='{range .status.allocation.devices.results[*]}{.driver}{" "}{.device}{"\n"}{end}'
```

```text
gpu.nvidia.com gpu-0-mig-1g10gb-19-2
```

Compare the claim with the device visible in the container and with the Slurm
job:

```bash
kubectl exec -n slurm-bridge "$POD" -- nvidia-smi -L
kubectl exec -n slurm slurm-controller-0 -- \
  sh -c 'for j in $(squeue -h -o %i); do scontrol show job -d $j; done' | \
  grep -E 'AdminComment|GRES='
```

```text
GPU 0: NVIDIA H100 80GB HBM3 (UUID: GPU-2f44c076-5031-59e2-f578-ea4e457c775c)
  MIG 1g.10gb     Device  0: (UUID: MIG-81e414a3-4d14-5182-afc4-e6e95f3dfd78)
     Nodes=gpu-worker-1 CPU_IDs=0 Mem=256 GRES=gpu:mig-1g.10gb:1(IDX:0)
   AdminComment={"pods":"slurm-bridge/cuda-mig-k9xnw/920e654f-4d9d-4b81-a445-5ee5fb2939aa"}
```

The `IDX` is the position of the claimed device in the node's
`slurm-bridge.dra-gres-map`, offset by the profile's `firstIndex`. The UUID
reported by `nvidia-smi` matches the `uuid` attribute of the claimed device in
the ResourceSlice. Deleting the Pod cancels the Slurm job and removes the
ResourceClaim.

## Sharing a GPU Node

Bridge jobs use exclusive, whole-node allocations by default. A Pod that
requests one GPU or MIG device would otherwise hold the whole node, and other
GPU Pods would wait. To place several GPU Pods on the same node, set
`slurmjob.slinky.slurm.net/exclusive: "false"` on the workload's
[annotation source](workload.md#pod-grouping). Slurm still allocates distinct
devices to each job.

## Limitations

- Only static MIG is supported. Create the MIG layout before scheduling
  workloads; MIG devices that the DRA driver creates on demand (dynamic MIG) are
  not supported.
- A GPU in MIG mode does not satisfy full-GPU requests, and a request for more
  devices than a node has cannot be satisfied. Slurm keeps such jobs pending
  with reason `BadConstraints`, and their Pods remain `Pending`. Check the Slurm
  job's reason when a GPU Pod does not start.
- `DeviceClass.spec.extendedResourceName` aliases are not resolved. Use the
  implicit `deviceclass.resource.kubernetes.io/<class>` resource names.

## Troubleshooting

| Symptom                                                                         | Checks                                                                                                                                                                                                    |
| ------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Pod fails with `OutOfdeviceclass.resource.kubernetes.io/...` after binding      | Enable `DRAExtendedResource` on the API server, scheduler, controller manager, and kubelet. Without it `status.extendedResourceClaimStatus` is dropped and the kubelet sees no capacity for the resource. |
| No `gpu.nvidia.com` ResourceSlices for a bridge node                            | Check that the kubelet plugin runs on the node. It needs the `slinky.slurm.net/managed-node` toleration, and its node selector or affinity must match the node.                                           |
| ResourceSlices exist, but `scontrol show node` has no `gpu:` GRES               | Add `gpu` to Slurm's `GresTypes`, and check that a device profile selects the devices. MIG devices need [per-size profiles](#configure-mig-deviceclasses-and-profiles).                                   |
| Admission rejects the Pod: `selector does not match a supported device profile` | Configure a profile whose selector exactly matches the DeviceClass selector, including whitespace after YAML folding.                                                                                     |
| A node logs `OverlappingDRADeviceProfiles` events                               | Make the selectors of profiles for the same driver mutually exclusive.                                                                                                                                    |
| Pod stays `Pending` and the Slurm job reason is `BadConstraints`                | The request cannot fit on any node: more devices than a node has, or full GPUs on a GPU in MIG mode.                                                                                                      |
| MIG devices are missing after reconfiguring MIG                                 | Restart the kubelet plugin on the node, then check `nvidia-smi -L` on the host.                                                                                                                           |
