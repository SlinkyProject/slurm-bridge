#!/bin/sh
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

set -eu

bridge_kubectl() {
	kubectl --namespace="$BRIDGE_NAMESPACE" --request-timeout=30s --cache-dir=/tmp/kubectl-cache "$@"
}

# Select the installed components, including admission when the new values
# disable it. A missing installation or any API error must fail the hook.
bridge_kubectl scale deployments --selector="$BRIDGE_SELECTOR" --replicas=0

# Wait for the Deployment controllers to observe the scale change, then for
# every pod to disappear. Ready replicas alone do not include terminating pods.
# The Job's activeDeadlineSeconds bounds the entire shutdown operation.
while :; do
	pending=$(bridge_kubectl get deployments --selector="$BRIDGE_SELECTOR" \
		-o go-template='{{range .items}}{{if or .spec.replicas .status.replicas (ne .metadata.generation .status.observedGeneration)}}{{.metadata.name}}{{"\n"}}{{end}}{{end}}')
	pods=$(bridge_kubectl get pods --selector="$BRIDGE_SELECTOR" -o name)
	if [ -z "$pending" ] && [ -z "$pods" ]; then
		echo "All Slurm Bridge components have stopped."
		break
	fi
	echo "Waiting for Slurm Bridge Deployments and pods to stop..."
	sleep 2
done
