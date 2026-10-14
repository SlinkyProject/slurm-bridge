# Upgrade policy

Slurm Bridge upgrades require a maintenance window. Stop all scheduler,
controller, and admission replicas before applying new bridge images or
configuration. Running different bridge versions concurrently during an upgrade
is not supported, including when leader election is enabled.

The Helm chart enables a `pre-upgrade` hook by default. It scales the installed
bridge Deployments in the release's namespace to zero, waits for their
controllers to observe the change, and waits until all matching pods have been
deleted. Only then does Helm apply the new release and restore the replica
counts from its values. The hook also stops an existing admission Deployment
when the new values disable admission. Initial installations do not run it.

The hook selects Deployments and pods by release name and bridge component
labels. Keep these labels and the installation namespace stable across an
upgrade. Suspend any external autoscaler or GitOps reconciliation that would
restore replicas while the hook is stopping the bridge.

## Maintenance window

Pause workload submissions and changes for the duration of the upgrade. While
the bridge is stopped:

- New bridge workloads cannot be scheduled, and bridge reconciliation pauses.
- Pod creation and updates covered by the admission webhooks can fail until
  admission is available again.
- The hook does not delete workload pods, cancel Slurm jobs, or stop Slurm
  daemons. Already running workloads can continue, but bridge reconciliation of
  their state resumes only after the upgrade.

Review the target release notes, Kubernetes and Slurm compatibility, and any
required migrations before upgrading. Save the current Helm values and release
revision for recovery. Configure `upgradeHook.image` with a reachable image
containing `/bin/sh` and a compatible `kubectl`; mirror it for restricted
registries if necessary. The hook uses its own ServiceAccount and namespace
Role, created before the Job, and runs on Kubernetes' `default-scheduler`.

For example, after setting `VERSION` to the target chart version:

```sh
helm upgrade slurm-bridge oci://ghcr.io/slinkyproject/charts/slurm-bridge \
  --namespace slurm-bridge --version "$VERSION" \
  --values values.yaml --wait --timeout 10m
```

`upgradeHook.activeDeadlineSeconds` defaults to 300 seconds and bounds the
hook's runtime, including scheduling and pod termination. Set Helm's `--timeout`
higher than this limit and allow enough time for the new components to become
ready. Resume workload submissions after the upgrade succeeds and the bridge
Deployments are ready.

## Failed upgrades and rollback

If scaling or waiting fails, Helm stops before applying the upgrade. Some or all
bridge Deployments may already be at zero replicas. Inspect the retained
`<release-fullname>-pre-upgrade` Job and its logs, resolve the failure, and
retry the upgrade. Successful hook resources are deleted; a retry removes
resources from the previous hook attempt before recreating them.

Do not assume a failed upgrade restores the previous replica counts. If the
upgrade is abandoned, first verify that the previous images and configuration
are still installed, then restore the scheduler, controller, and admission
replica counts recorded before the upgrade. If Helm has already applied part of
the new release, stop all bridge components again before restoring the previous
release.

The hook runs for `helm upgrade`, not `helm rollback`. Before rolling back,
manually scale all bridge Deployments to zero and wait for their pods to be
deleted. Check the target version's rollback and migration requirements before
restoring it. Automatic rollback options do not provide this shutdown sequence.

## Other deployment tools

Tools that only render Helm templates may not execute Helm hooks. Such workflows
must explicitly stop the bridge and wait for its pods to disappear before
applying an upgrade, then restore the desired replica counts. Set
`upgradeHook.enabled=false` only when the deployment workflow performs this
shutdown itself. The same requirement applies when using Helm's `--no-hooks`.

See [Helm's hook lifecycle](https://helm.sh/docs/topics/charts_hooks/) for hook
execution and cleanup semantics.
