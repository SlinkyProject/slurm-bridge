# ChangeLog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- Added support for LeaderWorkerSet
- Added support for setting placeholder UserId and GroupId
- Added managedNamespaceSelector to the admission controller

### Fixed

### Changed

### Removed

## v0.3.0

### Added

- Initial project release.

### Fixed

- Fixed generation of values-dev.yaml in hack/kind.sh
- Fixed setting placeholder cpu and memory from pod

### Changed

- Changed slurm-client version to 0.3.1
- Changed references from V0041 to V0043 data parser in pod controller
- Changed kind.sh to use Slurm dynamic nodes

### Removed
