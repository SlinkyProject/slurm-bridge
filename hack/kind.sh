#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

# https://kind.sigs.k8s.io/docs/user/quick-start/

set -euo pipefail

ROOT_DIR="$(readlink -f "$(dirname "$0")/..")"
SCRIPT_DIR="$(readlink -f "$(dirname "$0")")"

function kind::prerequisites() {
	go install sigs.k8s.io/kind@latest
}

# This section will make sure you don't run into issues from insufficient resources
# and have needed installed base software

function sys::check() {
	local fail=false
	if ! command -v docker >/dev/null 2>&1 && ! command -v podman >/dev/null 2>&1; then
		echo "'docker' or 'podman' is required:"
		echo "docker: https://www.docker.com/"
		echo "podman: https://podman.io/"
		fail=true
	fi
	if ! command -v go >/dev/null 2>&1; then
		echo "'go' is required: https://go.dev/"
		fail=true
	fi
	if ! command -v helm >/dev/null 2>&1; then
		echo "'helm' is required: https://helm.sh/"
		fail=true
	fi
	if ! command -v skaffold >/dev/null 2>&1; then
		echo "'skaffold' is required: https://skaffold.dev/"
		fail=true
	fi
	if ! command -v kubectl >/dev/null 2>&1; then
		echo "'kubectl' is recommended: https://kubernetes.io/docs/reference/kubectl/"
	fi
	if [[ $OSTYPE == "linux"* ]]; then
		if [ "$(/usr/sbin/sysctl -n kernel.keys.maxkeys)" -lt 2000 ]; then
			echo "Recommended to increase 'kernel.keys.maxkeys':"
			echo "  $ sudo sysctl -w kernel.keys.maxkeys=2000"
		fi
		if [ "$(/usr/sbin/sysctl -n fs.file-max)" -lt 10000000 ]; then
			echo "Recommended to increase 'fs.file-max':"
			echo "  $ sudo sysctl -w fs.file-max=10000000"
		fi
		if [ "$(/usr/sbin/sysctl -n fs.inotify.max_user_instances)" -lt 65535 ]; then
			echo "Recommended to increase 'fs.inotify.max_user_instances':"
			echo "  $ sudo sysctl -w fs.inotify.max_user_instances=65535"
		fi
		if [ "$(/usr/sbin/sysctl -n fs.inotify.max_user_watches)" -lt 1048576 ]; then
			echo "Recommended to increase 'fs.inotify.max_user_watches':"
			echo "  $ sudo sysctl -w fs.inotify.max_user_watches=1048576"
		fi
	elif [[ $OSTYPE == "darwin"* ]]; then
		# macOS: host file limits (Kind runs in a Linux VM; these affect host-side tooling).
		if [ "$(sysctl -n kern.maxfiles 2>/dev/null)" -lt 65536 ] 2>/dev/null; then
			echo "Recommended to increase 'kern.maxfiles':"
			echo "  $ sudo sysctl -w kern.maxfiles=65536"
		fi
		if [ "$(sysctl -n kern.maxfilesperproc 2>/dev/null)" -lt 65536 ] 2>/dev/null; then
			echo "Recommended to increase 'kern.maxfilesperproc':"
			echo "  $ sudo sysctl -w kern.maxfilesperproc=65536"
		fi
	fi

	if $fail; then
		exit 1
	fi
}

function kind::start() {
	sys::check
	kind::prerequisites
	local cluster_name="${1:-"kind"}"
	local kind_config="${2:-"$SCRIPT_DIR/kind.yaml"}"
	if ! kind get clusters 2>/dev/null | grep -Fxq "$cluster_name"; then
		if [ "$(command -v systemd-run)" ]; then
			CMD="systemd-run --scope --user"
		else
			CMD=""
		fi
		$CMD kind create cluster --name "$cluster_name" --config "$kind_config"
	fi
	kubectl config use-context kind-"$cluster_name"
	# Annotate external nodes with partition list (Kind node config does not support annotations).
	kubectl annotate nodes -l scheduler.slinky.slurm.net/external-node=true \
		scheduler.slinky.slurm.net/external-node-partitions=slurm-bridge --overwrite
	kubectl cluster-info --context kind-"$cluster_name"
}

function kind::delete() {
	local cluster_name="${1:-kind}"
	kind delete cluster --name "$cluster_name"
}

function helm::find() {
	local item="$1"
	if [ -z "$item" ]; then
		return 0
	elif [ "$(helm list --all-namespaces --short --filter="^${item}$" | wc -l)" -eq 0 ]; then
		return 1
	fi
	return 0
}

function slurm-bridge::install() {
	slurm-bridge::prerequisites
	echo "[slurm-bridge] Running skaffold (build and deploy slurm-bridge)..."
	(
		cd "$ROOT_DIR/helm/slurm-bridge"
		skaffold run
	)
}

function slurm-bridge::prerequisites() {
	local chartName

	# enables podgroup
	chartName="scheduler-plugins"
	if ! helm::find "$chartName"; then
		echo "[slurm-bridge] Installing scheduler-plugins..."
		helm install --repo https://scheduler-plugins.sigs.k8s.io "$chartName" "$chartName" \
			--namespace "$chartName" --create-namespace \
			--set 'plugins.enabled={CoScheduling}' --set 'scheduler.replicaCount=0'
	fi

	chartName="jobset"
	if ! helm::find "$chartName"; then
		echo "[slurm-bridge] Installing jobset..."
		local version="v0.8.x"
		helm install "$chartName" oci://registry.k8s.io/jobset/charts/jobset --version "$version" \
			--namespace "${chartName}-system" --create-namespace
	fi

	chartName="lws"
	if ! helm::find "$chartName"; then
		echo "[slurm-bridge] Installing lws (LeaderWorkerSet)..."
		local version="0.8.x"
		helm install "$chartName" oci://registry.k8s.io/lws/charts/lws \
			--version "$version" \
			--namespace "${chartName}-system" --create-namespace
	fi

	echo "[slurm-bridge] Installing slurm (operator + slurm chart)..."
	slurm::install
	echo "[slurm-bridge] Creating slurm-bridge secret and namespace..."
	slurm-bridge::secret
	kubectl create namespace slurm-bridge || true
}

function slurm::prerequisites() {
	helm repo add jetstack https://charts.jetstack.io
	helm repo update

	local chartName

	chartName="cert-manager"
	if ! helm::find "$chartName"; then
		echo "[slurm] Installing cert-manager..."
		helm install "$chartName" jetstack/cert-manager \
			--namespace "$chartName" --create-namespace --set crds.enabled=true
	fi
}

function slurm::install() {
	slurm::prerequisites

	local chartName
	local version="~1.1.0-rc1"

	chartName="slurm-operator"
	if ! helm::find "$chartName"; then
		echo "[slurm] Installing slurm-operator..."
		helm install "$chartName" oci://ghcr.io/slinkyproject/charts/slurm-operator \
			--version="$version" --namespace=slinky --create-namespace --wait \
			--set 'crds.enabled=true'
	fi
	# Wait for webhook to be ready so slurm chart install does not hit "connection refused"
	kubectl wait --for=condition=Available deployment/slurm-operator-webhook -n slinky --timeout=120s 2>/dev/null || true
	sleep 5

	chartName="slurm"
	if ! helm::find "$chartName"; then
		helm install "$chartName" oci://ghcr.io/slinkyproject/charts/slurm \
			--version="$version" --namespace=slurm --create-namespace --wait \
			--set "nodesets.slinky.enabled=false" \
			--set-string $'controller.extraConf=Nodeset=slurm-bridge Feature=slurm-bridge\nPartitionName=slurm-bridge Nodes=slurm-bridge State=UP Default=NO' \
			--set "controller.extraConfMap.ReconfigFlags=KeepPartInfo"
	fi
}

function extras::install() {
	helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update

	local chartName

	chartName="metrics-server"
	if ! helm::find "$chartName"; then
		helm install "$chartName" metrics-server/metrics-server \
			--set args="{--kubelet-insecure-tls}" \
			--namespace "$chartName" --create-namespace
	fi

	chartName="prometheus"
	if ! helm::find "$chartName"; then
		helm install "$chartName" prometheus-community/kube-prometheus-stack \
			--namespace "$chartName" --create-namespace --set installCRDs=true \
			--set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false
	fi

	chartName="keda"
	if ! helm::find "$chartName"; then
		helm install "$chartName" kedacore/keda \
			--namespace "$chartName" --create-namespace
	fi
}

function slurm-bridge::secret() {
	kubectl apply -f "${SCRIPT_DIR}"/token.yaml
}

function kjob::install() {
	local version="0.1.0"
	local kjob_path
	kjob_path=$(mktemp -d)
	git clone -b "v${version}" https://github.com/kubernetes-sigs/kjob.git "${kjob_path}"
	(
		cd "$kjob_path"
		make install
		make kubectl-kjob
		cp "./bin/kubectl-kjob" "$SCRIPT_DIR/kubectl-kjob"
	)
	kubectl create namespace slurm-bridge || true
	kubectl apply -f "${SCRIPT_DIR}"/kjob.yaml
	echo -e "\nRun the following command to install the kubectl kjob plugin:"
	echo -e "sudo cp ${SCRIPT_DIR}/kubectl-kjob /usr/local/bin/kubectl-kjob\n"
}

function dra-example-driver::install() {
	local cluster_name="${1:-kind}"
	local version="main"
	local dra_path
	dra_path=$(mktemp -d)
	git clone -b "$version" https://github.com/kubernetes-sigs/dra-example-driver.git "${dra_path}"
	(
		cd "$dra_path"

		# Build DRA images and load them into kind cluster.
		export KIND_CLUSTER_NAME="$cluster_name"
		./demo/build-driver.sh

		# Install with selectors and tolerations for slurm-bridge.
		local helm_chart="./deployments/helm/dra-example-driver/"
		cd $helm_chart
		cat <<EOF >./values-dev.yaml
kubeletPlugin:
  nodeSelector:
    scheduler.slinky.slurm.net/slurm-bridge: "worker"
  tolerations:
    - key: "slinky.slurm.net/managed-node"
      operator: "Equal"
      value: "slurm-bridge-scheduler"
      effect: "NoExecute"
EOF
		helm upgrade -i --create-namespace --namespace dra-example-driver \
			-f values.yaml -f values-dev.yaml \
			dra-example-driver .
	)
}

function dra-driver-cpu::install() {
	local cluster_name="${1:-kind}"
	local version="main"
	local dra_path
	dra_path=$(mktemp -d)
	git clone -b "$version" https://github.com/kubernetes-sigs/dra-driver-cpu.git "${dra_path}"
	(
		cd "$dra_path"
		local host_arch
		host_arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
		make manifests kind-install-cpu-dra CLUSTER_NAME="$cluster_name" PLATFORMS="linux/${host_arch}"
	)
	kubectl -n kube-system patch daemonsets.apps dracpu --type merge \
		-p '{"spec":{"template":{"spec":{"nodeSelector":{"scheduler.slinky.slurm.net/slurm-bridge":"worker"},"tolerations":[{"key":"slinky.slurm.net/managed-node","operator":"Equal","value":"slurm-bridge-scheduler","effect":"NoExecute"}]}}}}'
	kubectl -n kube-system patch daemonset dracpu --type='json' \
		-p '[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"},{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=individual"]}]'
}

function main::help() {
	cat <<EOF
$(basename "$0") - Manage a kind cluster for a slurm-bridge slurm-bridge-demo

	usage: $(basename "$0") [--config=KIND_CONFIG_PATH]
	        [--recreate|--delete]
	        [--extras] [--bridge] [--kjob] [--dra-example-driver] [--dra-driver-cpu] [--all]
	        [-h|--help] [--debug] [KIND_CLUSTER_NAME]

OPTIONS:
	--config=PATH       Use the specified kind config when creating.
	--recreate          Delete the Kind cluster and continue.
	--delete            Delete the Kind cluster and exit.
	--extras            Install optional dependencies (metrics, prometheus, keda).
	--bridge            Install slurm-bridge
	--kjob              Install kjob CRDs and build kubectl-kjob
	--dra-driver-cpu    Install DRA driver: dra-driver-cpu
	--dra-example-driver Install DRA driver: dra-example-driver
	--all               Install all charts for slurm-bridge

HELP OPTIONS:
	--debug             Show script debug information.
	-h, --help          Show this help message.

EOF
}

function main() {
	if $OPT_DEBUG; then
		set -x
	fi
	local cluster_name="${1:-"kind"}"
	if $OPT_DELETE || $OPT_RECREATE; then
		kind::delete "$cluster_name"
		$OPT_DELETE && return
	fi

	kind::start "$cluster_name" "$OPT_CONFIG"

	make -C "$ROOT_DIR" values-dev || true

	if $OPT_EXTRAS; then
		extras::install
	fi
	if $OPT_DRA_DRIVER_CPU; then
		dra-driver-cpu::install "$cluster_name"
	fi
	if $OPT_DRA_EXAMPLE_DRIVER; then
		dra-example-driver::install "$cluster_name"
	fi
	if $OPT_BRIDGE; then
		slurm-bridge::install
	fi
	if $OPT_KJOB; then
		kjob::install
	fi
}

OPT_DEBUG=false
OPT_RECREATE=false
OPT_CONFIG="$SCRIPT_DIR/kind.yaml"
OPT_DELETE=false
OPT_BRIDGE=false
OPT_EXTRAS=false
OPT_DRA_DRIVER_CPU=false
OPT_DRA_EXAMPLE_DRIVER=false
OPT_KJOB=false

SHORT="+h"
LONG="all,recreate,config:,delete,debug,bridge,extras,kjob,dra-driver-cpu,dra-example-driver,help"
OPTS="$(getopt -a --options "$SHORT" --longoptions "$LONG" -- "$@")"
eval set -- "${OPTS}"
while :; do
	case "$1" in
	--debug)
		OPT_DEBUG=true
		shift
		;;
	--recreate)
		OPT_RECREATE=true
		shift
		;;
	--config)
		OPT_CONFIG="$2"
		shift 2
		;;
	--delete)
		OPT_DELETE=true
		shift
		;;
	--bridge)
		OPT_BRIDGE=true
		shift
		;;
	--extras)
		OPT_EXTRAS=true
		shift
		;;
	--kjob)
		OPT_KJOB=true
		shift
		;;
	--dra-driver-cpu)
		OPT_DRA_DRIVER_CPU=true
		shift
		;;
	--dra-example-driver)
		OPT_DRA_EXAMPLE_DRIVER=true
		shift
		;;
	--all)
		OPT_BRIDGE=true
		OPT_KJOB=true
		OPT_DRA_DRIVER_CPU=true
		OPT_DRA_EXAMPLE_DRIVER=true
		OPT_EXTRAS=true
		shift
		;;
	-h | --help)
		main::help
		shift
		exit 0
		;;
	--)
		shift
		break
		;;
	*)
		echo "Unknown option: $1" >&2
		exit 1
		;;
	esac
done
main "$@"
