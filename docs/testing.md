# Developing Slurm Bridge

## Local Kind cluster

The workstation needs Make, Docker, Kind, Skaffold, Helm, kubectl, and Go.
Create a development cluster and deploy the complete bridge stack:

```sh
make kind-start
```

Use `make deploy` for a one-time rebuild or `make debug` for Delve-enabled
images. Delete the cluster with:

```sh
make kind-stop
```

The generated `helm/slurm-bridge/values-dev.yaml` file is a sparse, untracked
override. Add only values needed for development. Skaffold resets the release to
the current chart defaults before applying these overrides. If an older file is
a full copy of `values.yaml`, replace it with `{}` so it does not pin defaults
from an older checkout.

## End-to-end tests

The end-to-end suite can exercise either supported Slurm node mode. External
mode is the default:

```sh
make kind-start test-e2e
```

Use a separate cluster for hybrid mode because an installed cluster cannot
switch node modes in place:

```sh
KUBECONFIG=/tmp/slurm-bridge-hybrid.kubeconfig \
BUILDX_CONFIG=/tmp/slurm-bridge-hybrid-buildx \
SKAFFOLD_CACHE_FILE=/tmp/slurm-bridge-hybrid-skaffold-cache \
KIND_CLUSTER_NAME=slurm-bridge-hybrid \
SLURM_NODE_MODE=hybrid \
make kind-start test-e2e
```

`SLURM_NODE_MODE` is also passed into the test process. The readiness feature
uses it to verify that the cluster actually contains external nodes or
DaemonSet-mode hybrid `slurmd` pods, as requested. Hybrid runs also include a
native `sbatch` feature labeled `slurm-node-mode=hybrid`, which verifies that a
job submitted directly to Slurm completes on one of those hybrid workers.

## Remote cluster

Install a compatible released Slinky stack first. The workstation running the
deployment needs Kubernetes API access, push access to a registry, and a
registry namespace that the cluster can pull from.

Select the Kubernetes context and registry, then deploy the local checkout:

```sh
export SKAFFOLD_KUBE_CONTEXT=my-remote-cluster
export SKAFFOLD_DEFAULT_REPO=ghcr.io/my-user
make deploy
```

Skaffold builds and pushes the bridge images, then Helm upgrades Slurm Bridge
using the current chart defaults and `values-dev.yaml` overrides. Existing
release values are not retained, so add any required cluster-specific values to
`values-dev.yaml` before deploying.

## Specialized fixtures

Install all optional Kind development fixtures:

```sh
./hack/kind.sh --extras slurm-bridge-dev
```

This installs the CPU, example GPU, and NVIDIA GPU DRA drivers. Each driver can
still be installed individually with `--dra-driver-cpu`, `--dra-example-driver`,
or `--dra-driver-nvidia-gpu`.

NVIDIA GPU DRA normally requires GPU-equipped workers with the NVIDIA driver
installed on the host. For local testing without GPUs, set `MOCK_NVML=true` to
install
[`nvml-mock`](https://github.com/NVIDIA/k8s-test-infra/tree/main/deployments/nvml-mock)
on the managed Kind workers before the NVIDIA DRA driver:

```sh
MOCK_NVML=true make kind-start
```

Examples remain individually selectable:

```sh
kubectl apply -f hack/examples/job/single.yaml
kubectl apply -f hack/examples/dra/gpu-example/job.yaml
kubectl apply -f hack/examples/dra/nvidia/job.yaml
```

## Demo

Create the core stack, install all optional fixtures, and run a curated set of
finite example workloads:

```sh
make demo-start
```

Watch the workloads and their corresponding Slurm jobs until interrupted with
`Ctrl+C`:

```sh
./hack/watch.sh --demo
```

For an annotated view, or an annotated split-screen view with `tmux`, run:

```sh
./hack/watch.sh --explain
./hack/watch.sh --explain --tmux
```

The watchers require `watch`; the split-screen mode also requires `tmux`.
Stopping a watcher does not remove the demo workloads. Remove them with:

```sh
make demo-stop
```

The cluster remains available for development. Delete it with `make kind-stop`.

Optional system diagnostics remain a direct command:

```sh
sudo ./hack/sysctl.sh
```
