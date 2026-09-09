# Developing Slurm Bridge

## Local Kind cluster

The workstation needs Make, Docker, Kind, Skaffold, Helm, kubectl, and Go.
Create a development cluster and deploy the complete bridge stack:

```sh
make kind-start
```

Use `make deploy` for a one-time rebuild. Delete the cluster with:

```sh
make kind-stop
```

The generated `helm/slurm-bridge/values-dev.yaml` file is a sparse, untracked
override. Add only values needed for development. Skaffold resets the release to
the current chart defaults before applying these overrides. If an older file is
a full copy of `values.yaml`, replace it with `{}` so it does not pin defaults
from an older checkout.

## End-to-end tests

The release 1.1 suite uses an existing cluster and the branch's Kubernetes 1.35
dependencies. Select the cluster explicitly when creating the stack and running
the tests:

```sh
export KIND_CLUSTER_NAME=slurm-bridge-e2e
export KUBECONFIG=/tmp/slurm-bridge-e2e.kubeconfig
export BUILDX_CONFIG=/tmp/slurm-bridge-e2e-buildx
export SKAFFOLD_CACHE_FILE=/tmp/slurm-bridge-e2e-skaffold-cache
make kind-start test-e2e
```

`kind-start` installs the CPU and example GPU DRA drivers. For hybrid workers,
use a separate cluster because an installed cluster cannot switch node modes:

```sh
export KIND_CLUSTER_NAME=slurm-bridge-hybrid-e2e
export KUBECONFIG=/tmp/slurm-bridge-hybrid-e2e.kubeconfig
export BUILDX_CONFIG=/tmp/slurm-bridge-hybrid-e2e-buildx
export SKAFFOLD_CACHE_FILE=/tmp/slurm-bridge-hybrid-e2e-skaffold-cache
export SLURM_NODE_MODE=hybrid
make kind-start test-e2e
```

After checking that workers are ready in the requested mode, independent
workload features run in parallel. Coverage includes admission routing, Pods,
Jobs, parallel Jobs, JobSets, LeaderWorkerSets, scheduler-plugins PodGroups,
Slurm annotations and resources, cancellation in both directions, and CPU and
example GPU DRA allocation and cleanup. Hybrid runs also submit a native Slurm
batch job. Native Kubernetes PodGroups and NVIDIA mock-GPU fixtures are excluded
from this backport.

Slurm may queue work while the suite needs more nodes than are available. Each
feature allows up to ten minutes for an allocation. JUnit results, JSON test
output, and failure diagnostics are saved under `e2e-artifacts/` by default;
override this with `E2E_ARTIFACTS_DIR`.

Use `E2E_RUN` to select a feature and `E2E_CLEANUP=false` to retain its
resources:

```sh
E2E_RUN='TestScheduling/Non-exclusive_DRA_resources_allocated_to_container$' \
E2E_CLEANUP=false \
make test-e2e
```

Cancellation features still delete their workload because deletion is the
behavior they validate. The suite never deletes the cluster; run
`make kind-stop` with the same cluster name and environment when finished.

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

This is equivalent to `--dra-driver-cpu --dra-example-driver`. Each flag can
still be used individually.

Examples remain individually selectable:

```sh
kubectl apply -f hack/examples/job/single.yaml
kubectl apply -f hack/examples/dra/gpu-example/job.yaml
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
./hack/demo_watch.sh
```

Stopping the watcher does not remove the demo workloads. Remove them with:

```sh
make demo-stop
```

The cluster remains available for development. Delete it with `make kind-stop`.

Optional system diagnostics remain a direct command:

```sh
sudo ./hack/sysctl.sh
```
