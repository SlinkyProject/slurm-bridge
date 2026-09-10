# Host-installed Hybrid Slurm Nodes

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [Host-installed Hybrid Slurm Nodes](#host-installed-hybrid-slurm-nodes)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Prerequisites](#prerequisites)
  - [Configure Slurm](#configure-slurm)
  - [Build Debian Packages](#build-debian-packages)
  - [Install `slurmd` on a Worker](#install-slurmd-on-a-worker)
    - [Release an External Node](#release-an-external-node)
    - [Copy Authentication Material](#copy-authentication-material)
    - [Install and Configure the Service](#install-and-configure-the-service)
  - [Verify Registration](#verify-registration)
  - [Operational Notes](#operational-notes)
  - [Troubleshooting](#troubleshooting)

<!-- mdformat-toc end -->

## Overview

A Kubernetes worker can run `kubelet` and a native, host-installed `slurmd` on
the same machine. This is useful when the host operating system is managed
outside Kubernetes or when native Slurm jobs need direct access to the host.

The registration sequence has two distinct parts:

1. `slurmd` registers itself as a dynamic node with `slurmctld`.
1. `slurm-bridge` observes that the Slurm node maps to a Kubernetes node,
   applies its managed-node taint, and begins reconciling the node's scheduling
   state.

`slurm-bridge` does not install or start `slurmd`, and its external-node label
must not be used for a native node. That label tells the bridge controller to
create and own a different kind of Slurm node with `State=External`.

This guide describes the Debian/Ubuntu package flow used by a working bare-metal
deployment. For a Kubernetes-managed `slurmd`, use a DaemonSet-mode
slurm-operator `Nodeset` instead, as described in
[Config](config.md#hybrid-nodes).

## Prerequisites

- A working Slurm controller and Slurm REST API connected to `slurm-bridge`.
- A non-control-plane Kubernetes worker with `systemd`, `kubelet`, and root
  administrative access.
- Matching Slurm and Kubernetes node names. If they differ, set the
  `slinky.slurm.net/slurm-nodename` label as described in the
  [QuickStart Guide](quickstart.md#pre-requisites).
- The same Slurm release on the controller and worker. Build packages for the
  worker's CPU architecture.
- Controller authentication material available to the worker. The examples below
  use the `auth/slurm` key referenced by a slurm-operator `Controller`.
- Network access from the worker to `slurmctld` on TCP 6817 and from the
  controller to `slurmd` on TCP 6818.
- A controller address reachable from the host network. A Kubernetes Service
  `ClusterIP` works only when node processes can route to Service addresses.

## Configure Slurm

The controller must support configless clients and dynamic node registration.
Configless operation requires `SlurmctldParameters=enable_configless`, while
dynamic nodes require `SelectType=select/cons_tres` and a `MaxNodeCount` large
enough for the expected workers. The slurm-operator chart configures these
basics, but verify them when using another Slurm deployment.

Configure a feature-based `NodeSet` and a bridge-only partition before starting
the worker daemon:

```conf
ReconfigFlags=KeepPartInfo
NodeSet=slurm-bridge Feature=slurm-bridge
PartitionName=slurm-bridge Nodes=slurm-bridge State=UP Default=NO
```

The worker supplies `Feature=slurm-bridge` during dynamic registration. The
`NodeSet` maps that feature to the partition without predeclaring each worker
name. `ReconfigFlags=KeepPartInfo` preserves dynamic partition membership when
the controller reconfigures.

Use a partition dedicated to nodes that also exist in Kubernetes. Bridge jobs
must not be scheduled to Slurm-only nodes because `kubelet` launches the actual
pod after Slurm allocates the representative external job.

## Build Debian Packages

Build the exact Slurm version used by the controller on a compatible
Debian/Ubuntu system. The SchedMD source archive contains Debian packaging
metadata, so the essential flow is:

```bash
export SLURM_VERSION=26.05.1
export ARCH=amd64
export CACHE=/srv/slurm-packages/slurm-$SLURM_VERSION-$ARCH
export WORK=$CACHE/build

sudo apt-get update
sudo apt-get install -y --no-install-recommends \
  build-essential curl devscripts equivs fakeroot
sudo mkdir -p "$WORK"
sudo curl --fail --location \
  "https://download.schedmd.com/slurm/slurm-$SLURM_VERSION.tar.bz2" \
  --output "$WORK/slurm-$SLURM_VERSION.tar.bz2"
sudo tar -xaf "$WORK/slurm-$SLURM_VERSION.tar.bz2" -C "$WORK"

cd "$WORK/slurm-$SLURM_VERSION"
sudo mk-build-deps -i -t 'apt-get -y --no-install-recommends' debian/control
sudo debuild -b -uc -us
```

Retain these packages from the build output and make them available to every
target worker:

- `slurm-smd_<version>-1_<arch>.deb`
- `slurm-smd-client_<version>-1_<arch>.deb`
- `slurm-smd-slurmd_<version>-1_<arch>.deb`

A shared filesystem is convenient for several workers. Check for all three
artifacts before rebuilding so repeated runs can reuse the package cache.

## Install `slurmd` on a Worker

The following steps should be automated for every worker. Validate that each
target is a Kubernetes node, is not a control-plane node, has an `InternalIP`,
and resolves over SSH to the expected short hostname before modifying it.

### Release an External Node

If the node was previously registered in external-node mode, first transfer
ownership away from the bridge controller:

```bash
export NODE=worker-1

kubectl label node "$NODE" scheduler.slinky.slurm.net/external-node-
kubectl annotate node "$NODE" \
  scheduler.slinky.slurm.net/external-node-partitions-
```

Wait until `scontrol show node "$NODE"` either returns no node or no longer
reports `EXTERNAL`. Do not start the native daemon while the bridge controller
still owns the external node.

### Copy Authentication Material

For the default slurm-operator `auth/slurm` configuration, read the referenced
Secret instead of assuming its name or key:

```bash
export CONTROLLER_IP=$(kubectl get service -n slurm slurm-controller \
  -o jsonpath='{.spec.clusterIP}')
export KEY_SECRET=$(kubectl get controller.slinky.slurm.net -n slurm slurm \
  -o jsonpath='{.spec.slurmKeyRef.name}')
export KEY_FIELD=$(kubectl get controller.slinky.slurm.net -n slurm slurm \
  -o jsonpath='{.spec.slurmKeyRef.key}')
export KEY_FILE=$(mktemp)

kubectl get secret -n slurm "$KEY_SECRET" \
  -o "go-template={{ index .data \"$KEY_FIELD\" }}" | \
  base64 --decode >"$KEY_FILE"
chmod 600 "$KEY_FILE"
```

Copy the key and packages to the worker over a trusted administrative channel.
Delete the temporary copies after installation. If the controller uses another
authentication plugin, provision that plugin and its credentials instead.

### Install and Configure the Service

On the worker:

1. Stop an existing `slurmd` and remove any conflicting package that owns its
   systemd unit.
1. Install the `slurm-smd`, `slurm-smd-client`, and `slurm-smd-slurmd` packages.
1. Ensure the `slurm` user and group use the same IDs as the controller. The
   slurm-operator default is UID and GID 401; abort if another account already
   owns either ID.
1. Create `/etc/slurm`, `/var/lib/slurm`, `/var/log/slurm`, and
   `/var/spool/slurmd` owned by that account.
1. Install the authentication key as `/etc/slurm/slurm.key`, owned by `slurm`
   with mode `0600`.
1. Remove `/var/spool/slurmd/conf-cache` and recreate it with `slurm` ownership
   so stale controller configuration cannot survive a reinstall.

The controller-served configuration commonly refers to the controller by its
Kubernetes DNS name. Make that name resolvable from the host, for example:

```text
10.96.120.15 slurm-controller.slurm slurm-controller.slurm.svc slurm-controller.slurm.svc.cluster.local
```

Also ensure the worker's Slurm node name resolves to its Kubernetes
`InternalIP`. Prefer site DNS; `/etc/hosts` entries are sufficient for a small
or disposable cluster.

Configure the packaged systemd unit through `/etc/sysconfig/slurmd`:

```bash
SLURMD_OPTIONS="-Z --conf-server=10.96.120.15:6817 --conf=Feature=slurm-bridge"
```

The options have the following roles:

- `-Z` registers a dynamic node.
- `--conf-server` retrieves `slurm.conf` from the controller.
- `--conf=Feature=slurm-bridge` attaches the feature used by the controller's
  `NodeSet` and partition.

Some packages expect `/etc/slurm/slurm.conf` to exist even with configless
operation. An empty, `slurm`-owned file is sufficient when `--conf-server` is
passed. Confirm that `systemctl cat slurmd` includes
`EnvironmentFile=/etc/sysconfig/slurmd`; use an equivalent systemd drop-in if
the distribution unit does not.

Start and enable the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now slurmd
sudo systemctl is-active --quiet slurmd
sudo ss -ltn | grep -q ':6818'
```

## Verify Registration

Confirm the worker's binary version and inspect the node from the controller:

```bash
ssh "root@$NODE" /usr/sbin/slurmd -V
kubectl exec -n slurm slurm-controller-0 -- scontrol show node "$NODE"
kubectl exec -n slurm slurm-controller-0 -- sinfo -Nel
```

The node should report the expected Slurm version and `Feature=slurm-bridge`,
belong to the bridge partition, and not report `EXTERNAL`.

After the bridge observes matching Slurm and Kubernetes node names, it applies
its managed-node taint:

```bash
kubectl get node "$NODE" \
  -o jsonpath='{range .spec.taints[*]}{.key}={.value}:{.effect}{"\n"}{end}'
```

Expected output includes:

```text
slinky.slurm.net/managed-node=slurm-bridge-scheduler:NoExecute
```

Submit a small Kubernetes workload through `slurm-bridge` to verify the full
scheduling path. Also submit a native Slurm job if the installation is intended
to support both workload types. Never allow native and bridge-managed user
workloads to run on the same physical node at the same time; see
[Hybrid Workload Isolation](config.md#hybrid-workload-isolation).

## Operational Notes

- Host package installation, credentials, `slurmd`, and service upgrades remain
  the responsibility of the host automation. slurm-operator does not manage a
  host-installed daemon.
- Keep the external-node label absent. Reapplying it transfers node-registration
  ownership to the bridge controller and conflicts with the native daemon.
- Treat the controller key as a secret. Use mode `0600`, avoid logging it, and
  remove temporary copies even when installation fails.
- Clear the configless cache when moving a worker to another controller or when
  recovering from stale configuration.
- Ensure required Kubernetes system workloads tolerate the bridge-managed
  `NoExecute` taint.
- Install multiple workers in parallel only after all per-node validation has
  succeeded, and report a failure if any worker does not register.

## Troubleshooting

| Symptom                                     | Checks                                                                                                                                                 |
| ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `slurmd` does not start                     | Run `journalctl -u slurmd`; verify the key ownership, UID/GID, package version, and that the unit reads the options file.                              |
| Worker cannot register                      | Test TCP 6817 reachability, controller name resolution, authentication material, and the configless controller setting.                                |
| Controller cannot contact worker            | Verify `slurmd` listens on TCP 6818 and that host firewalls allow the controller.                                                                      |
| Node registers outside the bridge partition | Verify `Feature=slurm-bridge`, the feature-based `NodeSet`, and partition configuration.                                                               |
| Bridge does not taint the node              | Compare Kubernetes and Slurm node names, or set `slinky.slurm.net/slurm-nodename` on the Kubernetes node.                                              |
| Node remains `EXTERNAL`                     | Remove both external-node metadata fields and wait for the bridge controller to finish draining and deleting its node before starting native `slurmd`. |
