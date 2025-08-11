# Architecture

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Architecture](#architecture)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Pod Flowchart](#pod-flowchart)
  - [Directory Map](#directory-map)
    - [`cmd/`](#cmd)
    - [`config/`](#config)
    - [`docs/`](#docs)
    - [`hack/`](#hack)
    - [`helm/`](#helm)
    - [`internal/`](#internal)
    - [`internal/admission/`](#internaladmission)
    - [`internal/controller/`](#internalcontroller)
    - [`internal/scheduler/`](#internalscheduler)

<!-- mdformat-toc end -->

## Overview

This document describes the high-level architecture of the Slinky
`slurm-bridge`.

A pod scheduled by `slurm-bridge` will coordinate with Slurm to schedule a
placeholder job to represent the pod workload. The placeholder job uses the
external job capability in Slurm 25.05. An external Slurm job will be operate
like any other job in Slurm, with the exception that an external job will be
launched by something other than Slurm. In the case of `slurm-bridge`, the
placeholder job will determine where and when a pod run, but `kubelet` will
launch the pod instead of `slurmd`.

## Pod Flowchart

![Pod Flowchart](./_static/images/slurm-bridge_pod-flowchart.svg)

The above diagram represents the process of scheduling a pod with Slurm through
the following sequence:

1. A pod is applied to a configured `slurm-bridge` namespace.
1. The pod is sent to the `slurm-bridge` admission webhook.
1. The `slurm-bridge` scheduler begins placement of pod.
1. `slurm-bridge` coordinates with Slurm to create a “placeholder job”.
1. The placeholder job is scheduled to `node1`.
1. `slurm-bridge` determines the placeholder job has started on `node1`.
1. The scheduler binds the pod to `node1`.
1. kubelet starts the pod on `node1`.

During its lifecycle, the `slurm-bridge` controller will be reconciling events
from Kubernetes and Slurm.

## Directory Map

This project follows the conventions of:

- [Golang][golang-layout]
- [operator-sdk]
- [Kubebuilder]
- [scheduling-framework]

### `cmd/`

Contains code to be compiled into binary commands.

### `config/`

Contains yaml configuration files used for [kustomize] deployments.

### `docs/`

Contains project documentation.

### `hack/`

Contains files for development and Kubebuilder. This includes a kind.sh script
that can be used to create a kind cluster with all pre-requisites for local
testing.

### `helm/`

Contains [helm] deployments, including the configuration files such as
values.yaml.

Helm is the recommended method to install this project into your Kubernetes
cluster.

### `internal/`

Contains code that is used internally. This code is not externally importable.

### `internal/admission/`

Contains the admission webhook.

The webhook sets the scheduler name for pods created in the configured namespace
and enforces policy on labels and annotations used by the Slurm scheduler.

### `internal/controller/`

Contains the node and pod controllers.

The pod controller syncs the state of pods running in Kubernetes with the
associated placeholder job managed by Slurm, and vice versa. Similarly, the node
controller syncs node states between Kubernetes and Slurm.

### `internal/scheduler/`

Contains [scheduling framework][scheduling-framework] plugins. Currently, this
consists of slurm-bridge.

<!-- Links -->

[golang-layout]: https://go.dev/doc/modules/layout
[helm]: https://helm.sh/
[kubebuilder]: https://book.kubebuilder.io/
[kustomize]: https://kustomize.io/
[operator-sdk]: https://sdk.operatorframework.io/
[scheduling-framework]: https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/
