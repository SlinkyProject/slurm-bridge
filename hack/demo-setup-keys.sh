#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0
#
# Set kernel/sysctl values recommended for kind/demo (see hack/kind.sh sys::check).

set -euo pipefail

sudo sysctl -w kernel.keys.maxkeys=2000
echo 'kernel.keys.maxkeys=2000' | sudo tee --append /etc/sysctl.d/kernel

sudo sysctl -w fs.inotify.max_user_instances=65535
echo 'fs.inotify.max_user_instances=65535' | sudo tee --append /etc/sysctl.d/fs

sudo sysctl -w fs.inotify.max_user_watches=1048576
echo 'fs.inotify.max_user_watches=1048576' | sudo tee --append /etc/sysctl.d/fs
