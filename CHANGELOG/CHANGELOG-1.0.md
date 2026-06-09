## v1.0.4

### Added

- Use cosign to sign image artifacts.

### Fixed

- GO-2026-4918 GO-2026-4971 GO-2026-4976 GO-2026-4977 GO-2026-4980 GO-2026-4981
  GO-2026-4982 GO-2026-4986.
- GO-2026-5005 GO-2026-5006 GO-2026-5013 GO-2026-5014 GO-2026-5015 GO-2026-5016
  GO-2026-5017 GO-2026-5018 GO-2026-5019 GO-2026-5020 GO-2026-5021 GO-2026-5023
  GO-2026-5024 GO-2026-5025 GO-2026-5026 GO-2026-5027 GO-2026-5028 GO-2026-5029
  GO-2026-5030 GO-2026-5033.
- GO-2026-5037 GO-2026-5038 GO-2026-5039.
- Removed namespace from jwtKeyRef.

### Changed

- Slurm-bridge is now deployed to the slurm namespace instead of slinky.

### Miscellaneous

- Update go version for branch alignment.

## v1.0.3

### Fixed

- Updated google.golang.org/grpc to v1.79.3 to address CVE-2026-33186.

## v1.0.2

### Fixed

- Fixed edge case where the Kubernetes API drops reconcile requests such that
  the slurm-bridge controllers are unable to use that trigger to synchronize
  workloads, causing a desynchronized state and slurm-bridge scheduling may
  halt.

### Changed

- Update slurm-client version to v1.0.2.

## v1.0.1

### Fixed

- Update go toolchain to 1.25.5.
- Upgrade containerd to address CVE-2024-40635, CVE-2024-25621, and
  CVE-2025-64329.

### Changed

- Set enabled and disabled scheduler plugins explicitly and disable all by
  default with multipoint.
- Update slurm-client version to v1.0.1.

## v1.0.0

## v1.0.0-rc1

### Added

- Add arm64 support and multiarch manifest.
- Use PreEnqueue to add pod toleration instead of occurring in PreFilter.
- Error when slurmNode does not match any kubeNodes in preFilter stage.
- Include the default TaintToleration plugin as a Filter plugin.
- Add VolumeBinding plugin.
- Add an optional flag to install dra-example-driver in `hack/kind.sh`.
- Parse a pod's resources for DRA extended resource claims and translate it into
  a slurm GRES.
- Add new GetResources function in slurmcontrol to get node resources from Slurm
  for a given jobId.
- Support Extended Resource Claim in from a pod's resource request.
- Add example resources that use DRA Extended Resource Claims to initiate GRES
  requests and ResourceClaims.

### Fixed

- Fixed image rendering in `docs/index.rst`.
- Conversion of GHFM admonitions from `.md` to `.rst`.
- Update kubeVersion parsing to handle provider suffixes (e.g., GKE
  `x.y.z-gke.a`).
- Rename generate token secret to align with the installation guide.

### Changed

- Changed installation guide to not reference a version so the latest stable
  release is used.
- Create the Slurm placeholder job in PostFilter instead of PreFilter.
- Updated finalizer to be uniform with the rest of the Slinky namespaced key
  schema.
- Changed annotations that influence Slurm job create by adding a subnamespace
  component to the key (e.g. `slinky.slurm.net/job-name` =>
  `slurmjob.slinky.slurm.net/job-name`).
- Admission controller will reject pods with a ResourceClaim set.
- A ResourceClaim deletion to the pod controller for pods that are completed or
  being deleted.
- Ensure NodeResourcesFit Filter plugin does not run.
- Use v44 data parser for all slurm-control functions.

### Removed

- Remove unmaintained kustomize deployment. Helm and Skaffold are the preferred
  deployment for development and deployment.
- Remove Environment from the placeholder job submission struct as it is not
  required for external jobs.
