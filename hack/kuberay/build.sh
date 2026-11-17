#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

# Pin upstream native v1alpha3 Workload/PodGroup support for Kubernetes 1.37.
commit=5fd88bb817330d0a84246af9291d5b03169e9d98
archive_sha=94d46e701f9e19cc7a970a804e59927b124ae0e07145c19497cb51f05365e31e
artifact="${commit:0:7}-upstream"
build_root="${KUBERAY_BUILD_DIR:-/tmp/slurm-bridge-kuberay}/$artifact"
source_dir="$build_root/source"
image="slinky.local/kuberay:$artifact"

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
		curl --fail --location --retry 3 "https://codeload.github.com/ray-project/kuberay/tar.gz/$commit" -o "$archive"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		actual_sha=$(sha256sum "$archive" | awk '{print $1}')
	else
		actual_sha=$(shasum -a 256 "$archive" | awk '{print $1}')
	fi
	if [[ $actual_sha != "$archive_sha" ]]; then
		echo "KubeRay source checksum mismatch: $archive" >&2
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
echo "Building KubeRay revision $commit as $image" >&2
docker build --label "org.opencontainers.image.revision=$commit" --tag "$image" "$source_dir/ray-operator"
