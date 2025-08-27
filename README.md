# Slurm Bridge

<div align="center">

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg?style=for-the-badge)](./LICENSES/Apache-2.0.txt)
[![Tag](https://img.shields.io/github/v/tag/SlinkyProject/slurm-bridge?style=for-the-badge)](https://github.com/SlinkyProject/slurm-bridge/tags/)
[![Go-Version](https://img.shields.io/github/go-mod/go-version/SlinkyProject/slurm-bridge?style=for-the-badge)](./go.mod)
[![Last-Commit](https://img.shields.io/github/last-commit/SlinkyProject/slurm-bridge?style=for-the-badge)](https://github.com/SlinkyProject/slurm-bridge/commits/)

</div>

Run [Slurm] as a [Kubernetes] scheduler. A [Slinky] project.

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Slurm Bridge](#slurm-bridge)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Features](#features)
    - [Slurm](#slurm)
  - [Requirements](#requirements)
  - [Limitations](#limitations)
  - [Installation](#installation)
  - [Documentation](#documentation)
  - [License](#license)

<!-- mdformat-toc end -->

## Overview

[Slurm] and [Kubernetes] are workload managers originally designed for different
kinds of workloads. In broad strokes: Kubernetes excels at scheduling workloads
that typically run for an indefinite amount of time, with potentially vague
resource requirements, on a single node, with loose policy, but can scale its
resource pool infinitely to meet demand; Slurm excels at quickly scheduling
workloads that run for a finite amount of time, with well defined resource
requirements and topology, on multiple nodes, with strict policy, but its
resource pool is known.

This project enables the best of both workload managers. It contains a
[Kubernetes] scheduler to manage select workload from Kubernetes.

<img src="./docs/_static/images/slurm-bridge_big-picture.svg" alt="Slurm Bridge Architecture" width="100%" height="auto" />

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

## Requirements

- **Kubernetes Version**: >=
  [v1.29](https://kubernetes.io/blog/2023/12/13/kubernetes-v1-29-release/)
- **Slurm Version**: >=
  [25.05](https://www.schedmd.com/slurm-version-25-05-0-is-now-available/)

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
