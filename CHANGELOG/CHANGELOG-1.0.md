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
