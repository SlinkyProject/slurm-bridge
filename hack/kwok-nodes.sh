#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

COUNT="${1:-100}"

if [ "$#" -gt 1 ] || ! [[ $COUNT =~ ^[1-9][0-9]*$ ]]; then
	echo "usage: $(basename "$0") [COUNT]" >&2
	exit 1
fi

if ! kubectl --namespace kube-system get deployment kwok-controller >/dev/null 2>&1; then
	echo "KWOK is not installed. Run hack/kind.sh with --kwok first." >&2
	exit 1
fi

ARCH="$(kubectl get nodes --selector='!kwok.x-k8s.io/node' \
	--output=jsonpath='{.items[0].status.nodeInfo.architecture}')"

for ((index = 1; index <= COUNT; index++)); do
	printf -v node_name 'kwok-slurm-worker-%04d' "$index"
	if ((index % 2 == 0)); then
		topology="topo-switch:s2"
	else
		topology="topo-switch:s1"
	fi

	cat <<EOF
---
apiVersion: v1
kind: Node
metadata:
  name: ${node_name}
  annotations:
    kwok.x-k8s.io/node: fake
    node.alpha.kubernetes.io/ttl: "0"
    scheduler.slinky.slurm.net/external-node-partitions: slurm-bridge
    topology.slinky.slurm.net/spec: ${topology}
  labels:
    app.kubernetes.io/managed-by: slurm-bridge-kwok
    kubernetes.io/arch: ${ARCH}
    kubernetes.io/hostname: ${node_name}
    kubernetes.io/os: linux
    kwok.x-k8s.io/node: fake
    scheduler.slinky.slurm.net/external-node: "true"
    scheduler.slinky.slurm.net/slurm-bridge: worker
spec:
  taints:
    - effect: NoExecute
      key: slinky.slurm.net/managed-node
      value: slurm-bridge-scheduler
status:
  allocatable:
    cpu: "4"
    memory: 8Gi
    pods: "110"
  capacity:
    cpu: "4"
    memory: 8Gi
    pods: "110"
  nodeInfo:
    architecture: ${ARCH}
    kubeProxyVersion: fake
    kubeletVersion: fake
    operatingSystem: linux
  phase: Running
EOF
done | kubectl apply -f -
