# Running slurm-bridge locally

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Running slurm-bridge locally](#running-slurm-bridge-locally)
  - [Pre-requisites](#pre-requisites)
  - [Setting up your environment](#setting-up-your-environment)
  - [Installing `slurm-bridge` within your environment](#installing-slurm-bridge-within-your-environment)
  - [Cleaning up](#cleaning-up)

<!-- mdformat-toc end -->

You may want to run `slurm-bridge` on a single machine in order to test the
software or familiarize yourself with it prior to installing it on your cluster.
This should only be done for testing and evaluation purposes and should not be
used for production environments.

We have provided a script to do this using [Kind](https://kind.sigs.k8s.io/) and
the
[`hack/kind.sh`](https://github.com/SlinkyProject/slurm-bridge/blob/main/hack/kind.sh)
script.

This document assumes a basic understanding of
[Kubernetes architecture](https://kubernetes.io/docs/concepts/architecture/). It
is highly recommended that those who are unfamiliar with the core concepts of
Kubernetes review the documentation on
[Kubernetes](https://kubernetes.io/docs/concepts/overview/),
[pods](https://kubernetes.io/docs/concepts/workloads/pods/), and
[nodes](https://kubernetes.io/docs/concepts/architecture/nodes/) before getting
started.

## Pre-requisites

- [go 1.17+](https://go.dev/) must be installed on your system

## Setting up your environment

1. Install [Kind](https://kind.sigs.k8s.io/) using `go install`:

```bash
go install sigs.k8s.io/kind@v0.29.0
```

If you get `kind: command not found` when running the next step, you may need to
add GOPATH to your PATH:

```sh
export GOPATH=$HOME/go
export PATH=$PATH:$GOROOT/bin:$GOPATH/bin
```

2. Confirm that kind is working properly by running the following commands:

```bash
kind create cluster

kubectl get nodes --all-namespaces

kind delete cluster
```

3. Clone the
   [`slurm-bridge`](https://github.com/SlinkyProject/slurm-bridge/tree/main)
   repo and enter it:

```bash
git clone git@github.com:SlinkyProject/slurm-bridge.git
cd slurm-bridge
```

## Installing `slurm-bridge` within your environment

Provided with `slurm-bridge` is the script `hack/kind.sh` that interfaces with
kind to deploy the `slurm-bridge` helm chart within your local environment.

1. Create your cluster using `hack/kind.sh`:

```bash
hack/kind.sh --bridge
```

2. Familiarize yourself with and use your test environment:

```bash
kubectl get pods --namespace=slurm-bridge
kubectl get pods --namespace=slurm
kubectl get pods --namespace=slinky
```

At this point, you should have a kind cluster running `slurm-bridge`.

## Cleaning up

`hack/kind.sh` provides a mechanism by which to destroy your test environment.

Run:

```sh
hack/kind.sh --delete
```

To destroy your kind cluster.
