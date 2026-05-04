#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

# https://kind.sigs.k8s.io/docs/user/quick-start/

set -euo pipefail

ROOT_DIR="$(readlink -f "$(dirname "$0")/..")"
SCRIPT_DIR="$(readlink -f "$(dirname "$0")")"
SLURM_BRIDGE_TMP="/tmp/slurm-bridge-kind"
SLURM_NODE_MODE_EXTERNAL="external"
SLURM_NODE_MODE_HYBRID="hybrid"

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
	slurm-stack::check_node_mode "$OPT_SLURM_NODE_MODE"
	kind::configure_nodes "$OPT_SLURM_NODE_MODE"
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

function kind::configure_nodes() {
	local mode="$1"

	if [ "$mode" = "$SLURM_NODE_MODE_EXTERNAL" ]; then
		kubectl label nodes -l scheduler.slinky.slurm.net/slurm-bridge=worker \
			scheduler.slinky.slurm.net/external-node=true --overwrite
		# Annotate external nodes with partition list (Kind node config does not support annotations).
		kubectl annotate nodes -l scheduler.slinky.slurm.net/external-node=true \
			scheduler.slinky.slurm.net/external-node-partitions=slurm-bridge --overwrite
	fi
}

function slurm-stack::installed_node_mode() {
	if ! helm::find slurm; then
		return 0
	fi

	if kubectl get nodesets.slinky.slurm.net -n slurm \
		-o go-template='{{ range .items }}{{ .spec.scalingMode }} {{ index .spec.template.spec.nodeSelector "scheduler.slinky.slurm.net/slurm-bridge" }}{{ "\n" }}{{ end }}' 2>/dev/null |
		grep -q '^DaemonSet worker$'; then
		echo "$SLURM_NODE_MODE_HYBRID"
		return 0
	fi

	if kubectl get nodes \
		-l scheduler.slinky.slurm.net/slurm-bridge=worker,scheduler.slinky.slurm.net/external-node=true \
		-o name 2>/dev/null | grep -q .; then
		echo "$SLURM_NODE_MODE_EXTERNAL"
		return 0
	fi

	echo "unknown"
}

function slurm-stack::check_node_mode() {
	local mode="$1"
	local installed_mode
	installed_mode="$(slurm-stack::installed_node_mode)"

	if [ -z "$installed_mode" ] || [ "$installed_mode" = "$mode" ]; then
		return 0
	fi
	if [ "$installed_mode" = "unknown" ]; then
		echo "[slurm] Slurm is already installed, but the slurm node mode could not be inferred." >&2
	else
		echo "[slurm] Existing slurm node mode is $installed_mode, requested $mode." >&2
	fi
	echo "[slurm] Recreate the kind cluster before switching slurm node modes." >&2
	echo "[slurm]   $(basename "$0") --recreate --slurm-node-mode=$mode --bridge" >&2
	exit 1
}

function git::checkout() {
	local name="$1"
	local repo="$2"
	local ref="$3"
	local path="${SLURM_BRIDGE_TMP}/${name}"

	mkdir -p "$SLURM_BRIDGE_TMP"
	if [ ! -d "$path/.git" ]; then
		echo "[git] Cloning ${name} ${ref} to ${path}..." >&2
		git clone -b "$ref" "$repo" "$path" >&2
	else
		echo "[git] Updating ${name} ${ref} in ${path}..." >&2
		if ! (
			git -C "$path" fetch --tags origin &&
				git -C "$path" checkout "$ref" &&
				{
					# Tags leave the checkout detached, so only pull branch refs.
					if [ -n "$(git -C "$path" branch --show-current)" ]; then
						git -C "$path" pull --ff-only
					fi
				}
		) >&2; then
			echo "[git] Failed to update ${name} checkout at ${path}." >&2
			echo "[git] Remove the cached checkout and retry:" >&2
			echo "[git]   rm -rf ${path}" >&2
			exit 1
		fi
	fi

	echo "$path"
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
	scheduler-plugins::install
	jobset::install
	lws::install

	echo "[slurm-bridge] Installing slurm (operator + slurm chart)..."
	slurm-stack::install
	echo "[slurm-bridge] Creating slurm-bridge secret and namespace..."
	slurm-bridge::secret
	kubectl create namespace slurm-bridge || true
}

function scheduler-plugins::install() {
	local chartName
	chartName="scheduler-plugins"
	if ! helm::find "$chartName"; then
		echo "[slurm-bridge] Installing scheduler-plugins..."
		helm install "$chartName" "$chartName" \
			--repo https://scheduler-plugins.sigs.k8s.io \
			--namespace "$chartName" --create-namespace \
			--set 'plugins.enabled={CoScheduling}' \
			--set 'scheduler.replicaCount=0'
	fi
}

function jobset::install() {
	local chartName
	chartName="jobset"
	if ! helm::find "$chartName"; then
		echo "[slurm-bridge] Installing jobset..."
		local version="v0.8.x"
		helm install "$chartName" oci://registry.k8s.io/jobset/charts/jobset \
			--version "$version" --namespace "${chartName}-system" --create-namespace
	fi
}

function lws::install() {
	local chartName
	chartName="lws"
	if ! helm::find "$chartName"; then
		echo "[slurm-bridge] Installing lws (LeaderWorkerSet)..."
		local version="0.8.x"
		helm install "$chartName" oci://registry.k8s.io/lws/charts/lws \
			--version "$version" --namespace "${chartName}-system" --create-namespace
	fi
}

function slurm-stack::prerequisites() {
	local chartName
	chartName="cert-manager"
	if ! helm::find "$chartName"; then
		echo "[slurm] Installing cert-manager..."
		helm install "$chartName" oci://quay.io/jetstack/charts/cert-manager \
			--namespace "$chartName" --create-namespace \
			--set 'crds.enabled=true'
	fi
}

function slurm-stack::install() {
	local operator_path
	local ref="$OPT_SLURM_OPERATOR_REF"
	local repo="https://github.com/SlinkyProject/slurm-operator.git"

	slurm-stack::prerequisites

	operator_path="$(git::checkout slurm-operator "$repo" "$ref")"
	make -C "$operator_path" values-dev
	slurm-operator::install_from_source "$operator_path"
	slurm::install_from_source "$operator_path"

	slurm::configure_for_bridge "$operator_path/helm/slurm"
}

function slurm-operator::install_from_source() {
	local operator_path="$1"

	echo "[slurm] Installing slurm-operator..."
	(
		cd "$operator_path/helm/slurm-operator"
		sed -i.bak '/^crds:$/,/^[^[:space:]]/ s/^\([[:space:]]*enabled:[[:space:]]*\)false/\1true/' values-dev.yaml
		skaffold run
	)
	slurm-operator::wait
}

function slurm-operator::wait() {
	kubectl wait --for=condition=Available deployment/slurm-operator-webhook \
		-n slinky --timeout=120s
}

function slurm::install_from_source() {
	local operator_path="$1"

	echo "[slurm] Installing Slurm..."
	(
		cd "$operator_path/helm/slurm"
		skaffold run
	)
}

function slurm::configure_for_bridge() {
	local chart="$1"
	local chartName="slurm"

	echo "[slurm] Configuring Slurm for slurm-bridge..."
	helm upgrade "$chartName" "$chart" \
		--namespace slurm --create-namespace \
		--reuse-values \
		--wait \
		--set "nodesets.slinky.enabled=false" \
		--set-string $'controller.extraConf=Nodeset=slurm-bridge Feature=slurm-bridge\nPartitionName=slurm-bridge Nodes=slurm-bridge State=UP Default=NO' \
		--set "controller.extraConfMap.ReconfigFlags=KeepPartInfo"
}

function extras::install() {
	helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update

	local chartName

	chartName="metrics-server"
	if ! helm::find "$chartName"; then
		helm install "$chartName" metrics-server/metrics-server \
			--namespace "$chartName" --create-namespace \
			--set args="{--kubelet-insecure-tls}"
	fi

	chartName="prometheus"
	if ! helm::find "$chartName"; then
		helm install "$chartName" prometheus-community/kube-prometheus-stack \
			--namespace "$chartName" --create-namespace \
			--set installCRDs=true \
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
	local repo="https://github.com/kubernetes-sigs/kjob.git"
	kjob_path="$(git::checkout kjob "$repo" "v${version}")"
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
	local repo="https://github.com/kubernetes-sigs/dra-example-driver.git"
	dra_path="$(git::checkout dra-example-driver "$repo" "$version")"
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
  numDevices: 4
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
	local version="v0.1.0"
	local dra_path
	local repo="https://github.com/kubernetes-sigs/dra-driver-cpu.git"
	dra_path="$(git::checkout dra-driver-cpu "$repo" "$version")"
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
	        [--extras] [--bridge] [--slurm-node-mode=MODE]
	        [--slurm-operator-ref=REF] [--kjob]
	        [--dra-example-driver] [--dra-driver-cpu] [--all]
	        [-h|--help] [--debug] [KIND_CLUSTER_NAME]

OPTIONS:
	--config=PATH       Use the specified kind config when creating.
	--recreate          Delete the Kind cluster and continue.
	--delete            Delete the Kind cluster and exit.
	--extras            Install optional dependencies (metrics, prometheus, keda).
	--bridge            Install slurm-bridge
	--slurm-node-mode=MODE
	                    Configure Slurm nodes as external or hybrid. Default: $OPT_SLURM_NODE_MODE.
	--slurm-operator-ref=REF
	                    Clone slurm-operator from REF. Default: $OPT_SLURM_OPERATOR_REF.
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
OPT_SLURM_OPERATOR_REF="v1.1.0"
OPT_SLURM_NODE_MODE="$SLURM_NODE_MODE_EXTERNAL"

SHORT="+h"
LONG="all,recreate,config:,delete,debug,bridge,extras,kjob,dra-driver-cpu,dra-example-driver,slurm-operator-ref:,slurm-node-mode:,help"
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
	--slurm-node-mode)
		OPT_SLURM_NODE_MODE="$2"
		case "$OPT_SLURM_NODE_MODE" in
		"$SLURM_NODE_MODE_EXTERNAL" | "$SLURM_NODE_MODE_HYBRID") ;;
		*)
			echo "--slurm-node-mode must be one of: $SLURM_NODE_MODE_EXTERNAL, $SLURM_NODE_MODE_HYBRID" >&2
			exit 1
			;;
		esac
		shift 2
		;;
	--slurm-operator-ref)
		OPT_SLURM_OPERATOR_REF="$2"
		if [ -z "$OPT_SLURM_OPERATOR_REF" ]; then
			echo "--slurm-operator-ref requires a non-empty REF" >&2
			exit 1
		fi
		shift 2
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
