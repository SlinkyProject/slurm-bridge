## v1.2.0

### Fixed

- Fixed kind.sh nodes not having the necessary GresType for the example gpu dra
  driver.
- Omit empty CPU DRA requests and fail on incomplete allocations.
- Adds namespace:get RBAC to admission controller.
- Remove pod RBAC for admission controller.
- Remove node:update RBAC for node controller.
- Removed deviceclasses RBAC from node controller.
- Removed pods:update RBAC for pod controller.
- Added pods/finalizers rbac to pod controller.
- Removed unnecessary core RBAC.
- Removed volumeattachment RBAC.
- Removed RBAC for bindings.
- Removed RBAC for NodeResourceTopologies.
- Remove RBAC for workload status subresources.
- Remove create/list/watch from workload RBAC.
- Fix apigroup for SubjectAccessReviews.
- Removed node:patch RBAC from scheduler.
- Removed pod:delete RBAC from scheduler.
- Removed pods/finalizers:update RBAC from scheduler.
- Removed pods/status:update RBAC from scheduler.
- Removed resourceclaims/binding RBAC from scheduler.
- Removed resourceclaims/status:update RBAC from scheduler.
- Fix pods going into unschedulable queue for situations in which they should
  stay in the backoffq/activeq.
- Resolved DRA prebind when kube and Slurm node names differ and allocation
  pools do not match node names.
- Fixed config changes not rolling out after a helm upgrade.
- Skip empty DRA claims and use pod resource names in mappings.
- Match DRA mappings only to full device class names.

### Changed

- Disabled SchedulerPopFromBackoffQ.

## v1.2.0-rc1

### Added

- Added pods/finalizers resource to RBAC rules.
- Use cosign to sign image artifacts.
- Generate an SBOM that is included in OCI artifacts.
- Added priority as an annotation for workloads scheduled by Slurm-bridge.
- Added Helm unittests.
- Add dynamic topology reconciliation for external Slurm nodes.
- Added Kind topology example configuration for external and hybrid bridge
  nodes.
- Merge Slurm job annotations from Workload, Job, and PodGroup for PodGroup
  scheduling.
- Add documentation to support hybrid or external node configuration.

### Fixed

- Don't delete placeholder jobs for terminating pods.
- Updated google.golang.org/grpc to v1.79.3 to address CVE-2026-33186.
- Fixed cases where an out of bounds panic could occur.
- Fix external node creation with correct CPU topology.
- Update opentelemetry to resolve CVE-2026-39883.
- Fixed reconciliation of Slurm nodes to prevent the deletion of non-external
  nodes.
- GO-2026-4864 GO-2026-4865 GO-2026-4866 GO-2026-4869 GO-2026-4870 GO-2026-4946
  GO-2026-4947.
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
- Fixed nodeSelector and affinity fields for deployments.
- Fix behavior so that external node feature update no longer triggers
  reconfigure.
- Fix namespace override.
- Fix external job update code path from being unreachable.
- Protect against a race condition where an external job update may occur but
  the external job has started.
- Allow pods to be annotated if an external job update race occurs and the job
  is already running.

### Changed

- Slurm-bridge is now deployed to the slurm namespace instead of slinky.
