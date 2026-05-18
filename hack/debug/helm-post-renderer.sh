#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

# Helm post-renderer: apply the debug Kustomize overlay to rendered chart output.

set -euo pipefail

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

cp -R "$script_dir/kustomize/." "$tmpdir/"
cat >"$tmpdir/rendered.yaml"

kubectl kustomize "$tmpdir"
