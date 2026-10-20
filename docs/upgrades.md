# Upgrade policy

Slurm Bridge upgrades require downtime. The Helm chart's `pre-upgrade` hook
scales the scheduler, controllers, and admission Deployments to zero and waits
for their pods to exit before applying the upgrade. Helm then restores the
configured replica counts.

Pause workload submissions until the upgrade completes. Scheduling and
reconciliation stop, and admission requests may fail during the upgrade.
Existing workload pods and Slurm jobs are not stopped by the hook.

If the hook fails, the upgrade stops and bridge components may remain scaled
down. Check the hook Job's logs, resolve the failure, and retry.

For rollbacks or deployments that skip Helm hooks, stop the bridge and wait for
its pods to exit before applying changes.
