# Upgrading Slurm Bridge

## Supported upgrade paths

Within a major version, Slurm Bridge supports patch upgrades within the same
minor release and upgrades to the next consecutive minor release. **Skipping
minor releases is not supported.**

| Upgrade            | Supported path                          |
| :----------------- | :-------------------------------------- |
| `1.2.2` to `1.2.3` | Upgrade directly within the same minor. |
| `1.2` to `1.3`     | Upgrade directly to the next minor.     |
| `1.1` to `1.3`     | Upgrade to `1.2` first, then to `1.3`.  |

Use the latest patch release in each minor series. Complete and verify each step
before proceeding to the next minor release.

## Before upgrading

Check the installed release's chart version and application version:

```sh
helm get metadata slurm-bridge --namespace slurm --output json
```

Compare these versions with the intended target release. Helm's revision number
counts release operations; it is not the Slurm Bridge version. Keep the chart
and all three bridge component images on the intended application release.

Review the target release's configuration changes and confirm that your
Kubernetes and Slurm versions are compatible with both releases. After each
upgrade, wait for all bridge components to become ready and verify that workers
are registered with Slurm and a test workload can run and release its
allocation.

The Helm chart and running components do not currently reject skipped minor
versions. A successful Helm upgrade or healthy pod does not establish that an
upgrade path is supported. Check the source and target versions before applying
the upgrade.

For release validation on a dedicated cluster, the `TestUpgrade` e2e spec checks
the version path before changing the release, preserves running workloads across
the upgrade, and verifies that queued and new work can run afterward.
