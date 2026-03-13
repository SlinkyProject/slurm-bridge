## v1.1.0-rc1

### Added

- Documented use of Kyverno policies for Slurm-bridge
- Added support for DRA CPU driver `dra-driver-cpu`.
- The node controller will register labeled k8s nodes as Slurm external nodes.
- Add an annotation to control if a placeholder job runs in exclusive mode.
- Add in NVIDIA GPU handling for the NVIDIA GPU driver.

### Fixed

- Update go toolchain to 1.25.5.
- Upgrade containerd to address CVE-2024-40635, CVE-2024-25621, and
  CVE-2025-64329.
- Fixed edge case where the Kubernetes API drops reconcile requests such that
  the slurm-bridge controllers are unable to use that trigger to synchronize
  workloads, causing a desynchronized state and slurm-bridge scheduling may
  halt.
- Admission controller will process pods that have explicitly set schedulerName
  to the slurm-bridge scheduler.
- Mutating webhook unsets nodeName, if pre-defined, to ensure that we schedule.
- Don't return reconcile errors for benign not found errors.

### Changed

- Use CycleState to store slurmJobIR instead of repopulating slurmJobIR in the
  various scheduler plugins.
- Set enabled and disabled scheduler plugins explicitly and disable all by
  default with multipoint.
- Update slurm-client version to v1.0.1.
- Set the default managedNamespace to slurm-bridge.
- Update slurm-client to v1.1.0-rc1.
