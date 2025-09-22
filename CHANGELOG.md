# ChangeLog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

### Fixed

- Update kubeVersion parsing to handle provider suffixes (e.g., GKE
  x.y.z-gke.a).

### Changed

### Removed

## v0.4.0

### Added

- Added GPU device plugin parsing into a GRES request

### Fixed

### Changed

- Document new slurm-operator Token CR and add it to hack/kind.sh

### Removed

- Removed the need to specify --bridge when running hack/kind.sh
