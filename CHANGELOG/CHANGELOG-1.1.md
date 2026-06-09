## v1.1.1

### Added

- Added pods/finalizers resource to RBAC rules.
- Use cosign to sign image artifacts.

### Fixed

- Update opentelemetry to resolve CVE-2026-39883.
- Fixed reconciliation of Slurm nodes to prevent the deletion of non-external
  nodes.
- GO-2026-4918 GO-2026-4971 GO-2026-4976 GO-2026-4977 GO-2026-4980 GO-2026-4981
  GO-2026-4982 GO-2026-4986.
- GO-2026-5005 GO-2026-5006 GO-2026-5013 GO-2026-5014 GO-2026-5015 GO-2026-5016
  GO-2026-5017 GO-2026-5018 GO-2026-5019 GO-2026-5020 GO-2026-5021 GO-2026-5023
  GO-2026-5024 GO-2026-5025 GO-2026-5026 GO-2026-5027 GO-2026-5028 GO-2026-5029
  GO-2026-5030 GO-2026-5033.
- Prevent pods with non-existent resource requests from being bound by
  slurm-bridge.
- Fixes bug whereby multiple ResourceClaims were created after claim bind
  failures.
- GO-2026-5037 GO-2026-5038 GO-2026-5039.
- Removed namespace from jwtKeyRef.

### Changed

- Slurm-bridge is now deployed to the slurm namespace instead of slinky.

## v1.1.0

### Fixed

- Updated google.golang.org/grpc to v1.79.3 to address CVE-2026-33186.
- Fixed cases where an out of bounds panic could occur.
- Fix external node creation with correct CPU topology.
- Don't delete placeholder jobs for terminating pods.

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
