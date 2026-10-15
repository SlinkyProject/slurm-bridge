#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

# Pin upstream support for the external allocator, smtLayout, and smtEnabled.
commit=009e907a3d27a2a4a008eaa7fd4df37a90d4d45e
archive_sha=6ce05ac8ff001e7edeec9cf5a2513c89a6a0b063f6d18a3349a8ff54d9be6413
artifact="${commit:0:7}-upstream"
build_root="${DRA_CPU_BUILD_DIR:-/tmp/slurm-bridge-dra-cpu}/$artifact"
source_dir="$build_root/source"
image="slinky.local/dra-driver-cpu:$artifact"

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
		curl --fail --location --retry 3 "https://codeload.github.com/kubernetes-sigs/dra-driver-cpu/tar.gz/$commit" -o "$archive"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		actual_sha=$(sha256sum "$archive" | awk '{print $1}')
	else
		actual_sha=$(shasum -a 256 "$archive" | awk '{print $1}')
	fi
	if [[ $actual_sha != "$archive_sha" ]]; then
		echo "CPU driver source checksum mismatch: $archive" >&2
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
echo "Building CPU DRA revision $commit as $image" >&2
docker build --label "org.opencontainers.image.revision=$commit" --tag "$image" "$source_dir"
