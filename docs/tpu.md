# TPU DRA

Slurm Bridge supports whole-host TPU requests through an `indexed-gres`
DeviceProfile. Install the
[Google TPU DRA driver](https://github.com/kubernetes-sigs/dra-driver-google-tpu),
enable `DRAExtendedResource`, and use a CDI-capable runtime. The driver needs
[tolerations for TPU and bridge-managed nodes](../hack/dra-driver-google-tpu/values.yaml).

Add this profile to the bridge Helm values, retaining any other profiles you
use:

```yaml
sharedConfig:
  deviceProfiles:
    - name: tpu.google.com
      driver: tpu.google.com
      selector: 'device.driver == "tpu.google.com"'
      backend:
        type: indexed-gres
        gresName: tpu
```

Add `tpu` to Slurm's `GresTypes`. [Hybrid nodes](config.md#hybrid-nodes) also
need matching indexed `gres.conf` entries. The profile selector must exactly
match the DeviceClass selector.

Apply the [alias DeviceClass](../hack/examples/dra/tpu/deviceclass-alias.yaml)
to request `google.com/tpu` through `spec.extendedResourceName`. The class
matches the TPU profile above. This lets KubeRay detect TPUs and advertise Ray
`TPU` resources, and lets LWS inject worker IDs and peer addresses. Use the
[RayJob](../hack/examples/dra/tpu/rayjob.yaml) or
[ordinary Job](../hack/examples/dra/tpu/job.yaml) as a starting point. Ordinary
Jobs and JobSets must supply TPU runtime coordination themselves.

Request **all chips on a host in one container** (four in the examples). Bridge
creates the ResourceClaim. Partial-host requests fail in driver preparation;
constrain placement to hosts with the requested chip count. Sharing is
unsupported. Multi-host workloads also need a provisioned physical slice, Slurm
placement constraints/topology, and worker coordination. Bridge does not
provision slices, and these tests do not validate multi-host execution. The
current homogeneous Ray/LWS gang reserves TPU GRES on every member's node,
including a CPU-only head or leader.

## Kind check

The fixture runs the
[pinned upstream driver](../hack/dra-driver-google-tpu/build.sh) with four fake
v4 devices per worker. Tests cover KubeRay/LWS detection, Slurm allocation,
claim mappings, CDI device injection, cleanup, and reuse. They use one TPU
worker plus a CPU head/leader; they do not run TPU computation.

```sh
export KUBECONFIG=/tmp/slinky-tpu.kubeconfig
export BUILDX_CONFIG=/tmp/slinky-tpu-buildx
export SKAFFOLD_CACHE_FILE=/tmp/slinky-tpu-skaffold-cache
export E2E_KUBE_CONTEXT=kind-slinky-tpu
./hack/kind.sh --core --dra-driver-google-tpu --kuberay slinky-tpu
E2E_LABELS=suite=scheduling E2E_RUN='^TestScheduling/TPU' make test-e2e
E2E_LABELS=suite=tpu E2E_RUN='^TestTPUAllocation$' make test-e2e
kind delete cluster --name slinky-tpu
```

For hybrid nodes, add `--slurm-node-mode=hybrid` to cluster creation and set
`SLURM_NODE_MODE=hybrid` on the test commands. See [Testing](testing.md) for
cluster setup options. Fake devices and Kind values are for local tests only.
