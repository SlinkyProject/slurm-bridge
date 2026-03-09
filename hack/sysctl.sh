#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0
#
# Set kernel/sysctl values recommended for kind/demo (see hack/kind.sh sys::check).
# Only updates when current value is less than the target. Changes are runtime-only (not persisted).
# Linux: inotify and key limits for host where containers run. macOS: file descriptor limits for host.

set -euo pipefail

if [[ "$(uname -s)" == "Linux" ]]; then
	# Match hack/kind.sh sys::check (Linux) recommendations
	current=$(sysctl -n kernel.keys.maxkeys 2>/dev/null || echo 0)
	if [ "$current" -lt 2000 ]; then
		sudo sysctl -w kernel.keys.maxkeys=2000
	fi

	current=$(sysctl -n fs.file-max 2>/dev/null || echo 0)
	if [ "$current" -lt 10000000 ]; then
		sudo sysctl -w fs.file-max=10000000
	fi

	current=$(sysctl -n fs.inotify.max_user_instances 2>/dev/null || echo 0)
	if [ "$current" -lt 65535 ]; then
		sudo sysctl -w fs.inotify.max_user_instances=65535
	fi

	current=$(sysctl -n fs.inotify.max_user_watches 2>/dev/null || echo 0)
	if [ "$current" -lt 1048576 ]; then
		sudo sysctl -w fs.inotify.max_user_watches=1048576
	fi
elif [[ "$(uname -s)" == "Darwin" ]]; then
	# macOS: no inotify (Linux-only). Kind runs in a Linux VM; these help host-side tooling.
	current=$(sysctl -n kern.maxfiles 2>/dev/null || echo 0)
	if [ "$current" -lt 65536 ]; then
		sudo sysctl -w kern.maxfiles=65536
	fi

	current=$(sysctl -n kern.maxfilesperproc 2>/dev/null || echo 0)
	if [ "$current" -lt 65536 ]; then
		sudo sysctl -w kern.maxfilesperproc=65536
	fi
fi
