#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

# Pin the upstream driver, including fake-device discovery and mixed-driver claims.
commit=47727e5a96d4b56feaf012ae6d4b9b563b0cb08e
archive_sha=2d9d420ba22fb1a603ecd42e7cbd9a4322b78707ef86f3b4c2675f6043da388d
artifact="${commit:0:7}-upstream"
build_root="${DRA_TPU_BUILD_DIR:-/tmp/slurm-bridge-dra-tpu}/$artifact"
source_dir="$build_root/source"
image="slinky.local/dra-driver-google-tpu:$artifact"

case "${1:---build}" in
--revision)
	printf '%s\n' "$commit"
	exit 0
	;;
--image)
	printf '%s\n' "$image"
	exit 0
	;;
--prepare | --build) ;;
*)
	echo "usage: $0 [--prepare|--build|--image|--revision]" >&2
	exit 2
	;;
esac

if [[ ! -f "$source_dir/.slinky-upstream" ]]; then
	mkdir -p "$build_root"
	archive="$build_root/source.tar.gz"
	if [[ ! -f $archive ]]; then
		curl --fail --location --retry 3 "https://codeload.github.com/kubernetes-sigs/dra-driver-google-tpu/tar.gz/$commit" -o "$archive"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		actual_sha=$(sha256sum "$archive" | awk '{print $1}')
	else
		actual_sha=$(shasum -a 256 "$archive" | awk '{print $1}')
	fi
	if [[ $actual_sha != "$archive_sha" ]]; then
		echo "TPU driver source checksum mismatch: $archive" >&2
		exit 1
	fi
	stage=$(mktemp -d "$build_root/source.XXXXXX")
	tar -xzf "$archive" -C "$stage" --strip-components=1
	touch "$stage/.slinky-upstream"
	mv "$stage" "$source_dir"
fi

if [[ ${1:---build} == --prepare ]]; then
	printf '%s\n' "$source_dir"
	exit 0
fi
export BUILDX_CONFIG="${BUILDX_CONFIG:-$build_root/buildx}"
echo "Building TPU DRA revision $commit as $image" >&2
docker build --label "org.opencontainers.image.revision=$commit" --tag "$image" --file "$source_dir/deployments/container/Dockerfile" "$source_dir"
