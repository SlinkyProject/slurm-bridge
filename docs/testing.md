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

On release 1.0, this is equivalent to `--dra-example-driver`. The CPU DRA driver
is not supported by this release family.

Examples remain individually selectable:

```sh
kubectl apply -f hack/examples/job/single.yaml
kubectl apply -f hack/examples/dra/job.yaml
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

## End-to-end tests

Run unit tests with `make test`. Run the E2E suite separately using the current
Kubernetes context with `make test-e2e`.

The development setup needs Docker, Kind, Skaffold, Helm, kubectl, GNU getopt,
and Go. Select a dedicated cluster and keep its client state together:

```sh
export KIND_CLUSTER_NAME=slurm-bridge-1-0-e2e
export KUBECONFIG=/tmp/${KIND_CLUSTER_NAME}.kubeconfig
export BUILDX_CONFIG=/tmp/${KIND_CLUSTER_NAME}-buildx
export SKAFFOLD_CACHE_FILE=/tmp/${KIND_CLUSTER_NAME}-skaffold-cache
export SLURM_NODE_MODE=external
make kind-start test-e2e
```

`kind-start` installs release-1.0 of Slurm Operator, Slurm, Slurm Bridge,
scheduler-plugins, JobSet, LeaderWorkerSet, and the example GPU DRA driver.
Skaffold configures `slurm-bridge` as a managed admission namespace.
`KIND_CONFIG` selects another Kind config; `KIND_NODE_IMAGE` selects the node
image when creating a cluster. The shared CI job uses `hack/kind.yaml`.

Set `SLURM_NODE_MODE=hybrid` to test real slurmd workers. Recreate the cluster
when changing node modes. Hybrid readiness resolves each Kubernetes worker's
`slinky.slurm.net/slurm-nodename` label to its Slurm node name and checks for a
Ready slurmd pod. The hybrid suite also submits and completes a native `sbatch`
job.

### Coverage

The suite checks readiness serially, then runs independent workload features in
parallel: admission routing, Pod scheduling, Job completion, parallel Jobs,
JobSets, scheduler-plugins PodGroups, LeaderWorkerSets, Slurm annotations and
resources, cancellation in both directions, and example GPU DRA allocation and
cleanup. Allocations can queue for up to ten minutes while other features run.

Release 1.0 always uses exclusive allocations. Native Kubernetes PodGroups, CPU
DRA, and non-exclusive allocation tests are omitted because those features are
absent from this release. The NVIDIA DRA fixture is also omitted.

Use a Go test expression to select a feature and optionally retain its
resources:

```sh
E2E_RUN='TestScheduling/Example_GPU_DRA_resources_allocated_to_container$' \
E2E_CLEANUP=false make test-e2e
```

Cancellation features still delete their workload during the test. Results go to
`e2e-artifacts/junit.xml` and `e2e-artifacts/test-output.json`; failed features
also collect cluster, workload, Slurm, and container diagnostics there. Override
`E2E_ARTIFACTS_DIR` to choose another destination.

`test-e2e` leaves the cluster running. Run `make kind-stop` to remove it.

### Existing clusters

Select the intended context explicitly before running `make test-e2e`. The
cluster must have the same controllers and example GPU DRA driver, a
`slurm-bridge` partition and managed namespace, bridge workers labeled
`scheduler.slinky.slurm.net/slurm-bridge=worker`, and a Slurm controller pod
named `slurm-controller-0` in namespace `slurm`. Match `SLURM_NODE_MODE` to the
installed configuration.
