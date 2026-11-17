# Upgrade policy

Slurm Bridge upgrades require downtime and an empty pending workload queue.
Carrying pending work across an upgrade, including `1.2` to `1.3`, is
unsupported.

Before upgrading:

1. Pause new workload submissions, including automated submissions and
   controllers that can create replacement Pods.
1. Keep the existing Bridge running while pending workloads finish scheduling,
   or cancel them through their owning workload. Deleting a Pod alone may cause
   its controller to replace it. Let Bridge clean up any canceled Slurm
   allocations.
1. Verify that no pending Bridge Pods or pending Bridge-owned Slurm allocations
   remain. Include gated Pods, held allocations and partially scheduled gangs.
   Running workloads may remain running.

List pending Pods using the installed `schedulerConfig.schedulerName`:

```sh
kubectl get pods --all-namespaces \
  --field-selector=spec.schedulerName=slurm-bridge-scheduler,status.phase=Pending
```

Check the Slurm queue separately for pending allocations belonging to this
Bridge installation. The Pod check cannot detect orphaned Slurm allocations.
Unrelated native Slurm jobs do not need to be drained.

For example, list pending jobs in the partitions used by this installation:

```sh
squeue --noheader --partition=slurm-bridge --states=PENDING \
  --format='%.18i %.9P %.30j %.8u %.20R'
```

Replace `slurm-bridge` with a comma-separated list of all partitions used by
Bridge workloads, including workload-specific partition overrides. A drained
pending queue has no rows for Bridge-owned jobs; held jobs are included in
`PENDING`. In shared partitions, identify which jobs belong to this installation
before canceling anything. Running jobs may remain.

The Helm chart's `pre-upgrade` hook first checks for pending Pods across all
namespaces using `schedulerConfig.schedulerName`, including scheduling-gated
Pods and bound Pods that have not started. Pending Pods or API errors fail the
hook before any Bridge component is stopped. The check requires cluster-wide
`get` and `list` access to Pods; the chart creates temporary hook RBAC for this
purpose. Keep the scheduler name unchanged during the upgrade so this check
covers the existing workloads.

After the check succeeds, the hook scales the scheduler, controllers, and
admission Deployments to zero and waits for their pods to exit before applying
the upgrade. Helm then restores the configured replica counts.

Keep workload submissions paused until the upgrade completes. Scheduling and
reconciliation stop, and admission requests may fail during the upgrade.
Existing workload pods and Slurm jobs are not stopped by the hook.

If the pending check fails, inspect the hook Pod's `check-pending-workloads`
container logs and list pending Pods with the command above. Drain the queue and
retry. A failed check leaves the existing components running. Failures in later
shutdown steps can leave components scaled down; inspect the failing container's
logs, resolve the failure, and retry.

To bypass only the pending Pod check, add this to your `helm upgrade` command:

```sh
--set upgradeHook.skipPendingCheck=true
```

Use this escape hatch only after independently verifying the drain requirement.
It preserves coordinated shutdown and does not make pending workloads supported
across upgrades. Reset it to `false` for subsequent upgrades. Setting
`upgradeHook.enabled=false` disables both the check and shutdown.

After upgrading, verify that all Bridge components are ready and a new test
workload can run and release its allocation, then resume submissions.

For rollbacks or deployments that skip Helm hooks, stop the bridge and wait for
its pods to exit before applying changes. Verify the drained queue manually when
hooks are skipped. The check is a point-in-time check, so submissions must
remain paused throughout the procedure.
