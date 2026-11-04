// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

################################################################################

variable "REGISTRY" {
  default = "ghcr.io/slinkyproject"
}

variable "VERSION" {
  default = "0.0.0"
}

function "format_tag" {
  params = [registry, stage, version]
  result = format("%s:%s", join("/", compact([registry, stage])), join("-", compact([version])))
}

################################################################################

target "_common" {
  labels = {
    # Ref: https://github.com/opencontainers/image-spec/blob/v1.0/annotations.md
    "org.opencontainers.image.authors" = "slinky@schedmd.com"
    "org.opencontainers.image.documentation" = "https://github.com/SlinkyProject/slurm-bridge"
    "org.opencontainers.image.license" = "Apache-2.0"
    "org.opencontainers.image.vendor" = "SchedMD LLC."
    "org.opencontainers.image.version" = VERSION
    "org.opencontainers.image.source" = "https://github.com/SlinkyProject/slurm-bridge"
    # Ref: https://docs.redhat.com/en/documentation/red_hat_software_certification/2025/html/red_hat_openshift_software_certification_policy_guide/assembly-requirements-for-container-images_openshift-sw-cert-policy-introduction#con-image-metadata-requirements_openshift-sw-cert-policy-container-images
    "vendor" = "SchedMD LLC."
    "version" = VERSION
    "release" = "https://github.com/SlinkyProject/slurm-bridge"
  }
}

target "_multiarch" {
  platforms = [
    "linux/amd64",
    "linux/arm64"
  ]
}

################################################################################

group "default" {
  targets = [
    "scheduler",
    "admission",
  ]
}

################################################################################

target "scheduler" {
  inherits = ["_common", "_multiarch"]
  dockerfile = "Dockerfile"
  target = "scheduler"
  labels = {
    # Ref: https://github.com/opencontainers/image-spec/blob/v1.0/annotations.md
    "org.opencontainers.image.title" = "Slurm Bridge Scheduler"
    "org.opencontainers.image.description" = "Slurm Bridge Scheduler"
    # Ref: https://docs.redhat.com/en/documentation/red_hat_software_certification/2025/html/red_hat_openshift_software_certification_policy_guide/assembly-requirements-for-container-images_openshift-sw-cert-policy-introduction#con-image-metadata-requirements_openshift-sw-cert-policy-container-images
    "name" = "Slurm Bridge Scheduler"
    "summary" = "Slurm Bridge Scheduler"
    "description" = "Slurm Bridge Scheduler"
  }
  tags = [
    format_tag(REGISTRY, "slurm-bridge-scheduler", VERSION),
  ]
}

################################################################################

target "admission" {
  inherits = ["_common", "_multiarch"]
  dockerfile = "Dockerfile"
  target = "admission"
  labels = {
    # Ref: https://github.com/opencontainers/image-spec/blob/v1.0/annotations.md
    "org.opencontainers.image.title" = "Slurm Bridge Admission Controller"
    "org.opencontainers.image.description" = "Slurm Bridge Admission Controller"
    # Ref: https://docs.redhat.com/en/documentation/red_hat_software_certification/2025/html/red_hat_openshift_software_certification_policy_guide/assembly-requirements-for-container-images_openshift-sw-cert-policy-introduction#con-image-metadata-requirements_openshift-sw-cert-policy-container-images
    "name" = "Slurm Bridge Admission Controller"
    "summary" = "Slurm Bridge Admission Controller"
    "description" = "Slurm Bridge Admission Controller"
  }
  tags = [
    format_tag(REGISTRY, "slurm-bridge-admission", VERSION),
  ]
}
