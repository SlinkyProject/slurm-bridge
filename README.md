# Slurm Bridge

Run [Slurm] as a [Kubernetes] scheduler. A [Slinky] project.

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Slurm Bridge](#slurm-bridge)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Features](#features)
    - [Slurm](#slurm)
  - [Compatibility](#compatibility)
  - [Limitations](#limitations)
  - [Installation](#installation)
  - [Documentation](#documentation)
  - [Support and Development](#support-and-development)
  - [License](#license)

<!-- mdformat-toc end -->

## Overview

[Slurm] and [Kubernetes] are workload managers originally designed for different
kinds of workloads. Kubernetes excels at scheduling workloads that run for an
indefinite amount of time, with potentially vague resource requirements, on a
single node, with loose policy, but can scale its resource pool infinitely to
meet demand; Slurm excels at quickly scheduling workloads that run for a finite
amount of time, with well-defined resource requirements and topology, on
multiple nodes, with strict policy, and a known resource pool.

This project enables the best of both workload managers. It contains a
[Kubernetes] scheduler to manage select workloads from Kubernetes, which allows
for co-location of Kubernetes and Slurm workloads within the same cluster. This
means the same hardware can be used to run both traditional HPC and cloud-like
workloads, reducing operating costs.

Using `slurm-bridge`, workloads can be submitted from within a Kubernetes
context as a `Pod`, `PodGroup`, `Job`, `JobSet`, or `LeaderWorkerSet` and from a
Slurm context using `salloc` or `sbatch`. Workloads submitted via Slurm will
execute as they would in a Slurm-only environment, using `slurmd`. Workloads
submitted from Kubernetes will have their resource requirements translated into
a representative Slurm job by `slurm-bridge`. That job will serve as a
placeholder and will be scheduled by the Slurm controller. Upon resource
allocation to a K8s workload by the Slurm controller, `slurm-bridge` will bind
the workload's pod(s) to the allocated node(s). At that point, the kubelet will
launch and run the pod the same as it would within a standard Kubernetes
instance

!["Slurm Bridge Architecture"](./docs/_static/images/slurm-bridge_big-picture.svg)

For additional architectural notes, see the [architecture] docs.

## Features

### Slurm

Slurm is a full featured HPC workload manager. To highlight a few features:

- [**Priority**][slurm-priority]: assigns priorities to jobs upon submission and
  on an ongoing basis (e.g. as they age).
- [**Preemption**][slurm-preempt]: stop one or more low-priority jobs to let a
  high-priority job run.
- [**QoS**][slurm-qos]: sets of policies affecting scheduling priority,
  preemption, and resource limits.
- [**Fairshare**][slurm-fairshare]: distribute resources equitably among users
  and accounts based on historical usage.

## Compatibility

| Software   |                             Minimum Version                              |
| :--------- | :----------------------------------------------------------------------: |
| Kubernetes | [v1.34](https://kubernetes.io/blog/2025/08/27/kubernetes-v1-34-release/) |
| Slurm      | [25.11](https://www.schedmd.com/slurm-version-25-11-0-is-now-available/) |

## Limitations

- Exclusive, whole node allocations are made for each pod.

## Installation

Create a secret for slurm-bridge to communicate with Slurm.

```sh
export SLURM_JWT=$(scontrol token username=slurm lifespan=infinite)
kubectl create namespace slurm-bridge
kubectl create secret generic slurm-bridge-jwt-token --namespace=slinky --from-literal="auth-token=$SLURM_JWT" --type=Opaque
```

Install the slurm-bridge scheduler:

```sh
helm install slurm-bridge oci://ghcr.io/slinkyproject/charts/slurm-bridge \
  --namespace=slinky --create-namespace
```

For additional instructions, see the [quickstart] guide.

## Documentation

Project documentation is located in the [docs] directory of this repository.

Slinky documentation can be found [here][slinky-docs].

## Support and Development

Feature requests, code contributions, and bug reports are welcome!

Github/Gitlab submitted issues and PRs/MRs are handled on a best effort basis.

The SchedMD official issue tracker is at <https://support.schedmd.com/>.

To schedule a demo or simply to reach out, please
[contact SchedMD][contact-schedmd].

## License

Copyright (C) SchedMD LLC.

Licensed under the
[Apache License, Version 2.0](http://www.apache.org/licenses/LICENSE-2.0) you
may not use project except in compliance with the license.

Unless required by applicable law or agreed to in writing, software distributed
under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.

<!-- Links -->

[architecture]: ./docs/architecture.md
[contact-schedmd]: https://www.schedmd.com/slurm-resources/contact-schedmd/
[docs]: ./docs/
[kubernetes]: https://kubernetes.io/
[quickstart]: ./docs/quickstart.md
[slinky]: https://slinky.ai/
[slinky-docs]: https://slinky.schedmd.com/
[slurm]: https://slurm.schedmd.com/overview.html
[slurm-fairshare]: https://slurm.schedmd.com/fair_tree.html
[slurm-preempt]: https://slurm.schedmd.com/preempt.html
[slurm-priority]: https://slurm.schedmd.com/priority_multifactor.html
[slurm-qos]: https://slurm.schedmd.com/qos.html
