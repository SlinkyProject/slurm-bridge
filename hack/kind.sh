#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

# https://kind.sigs.k8s.io/docs/user/quick-start/

set -euo pipefail

ROOT_DIR="$(readlink -f "$(dirname "$0")/..")"

SCRIPT_DIR="$(readlink -f "$(dirname "$0")")"
SLURM_BRIDGE_TMP="$(mktemp -d)"
trap 'rm -rf "$SLURM_BRIDGE_TMP"' EXIT
SLURM_NODE_MODE_EXTERNAL="external"
SLURM_NODE_MODE_HYBRID="hybrid"
LOCAL_PATH_PROVISIONER_CHART="oci://ghcr.io/rancher/local-path-provisioner/charts/local-path-provisioner"
LOCAL_PATH_PROVISIONER_VERSION="0.0.34"

MIN_KIND_VERSION="0.32.0"
MIN_SKAFFOLD_VERSION="2.18.0"

function tool::version_ge() {
	local have="$1"
	local need="$2"
	[[ "$(printf '%s\n' "$need" "$have" | sort -V | head -1)" == "$need" ]]
}

function tool::version() {
	local name="$1"
	case "$name" in
	kind)
		kind version 2>/dev/null | awk '{print $2}' | sed 's/^v//'
		;;
	skaffold)
		skaffold version 2>/dev/null | sed 's/^v//'
		;;
	*)
		echo "unknown tool: $name" >&2
		return 1
		;;
	esac
}

function tool::require_min_version() {
	local name="$1"
	local min_version="$2"
	local url="$3"
	if ! command -v "$name" >/dev/null 2>&1; then
		echo "'$name' is required: $url" >&2
		return 1
	fi
	local have
	have="$(tool::version "$name")"
	if [ -z "$have" ]; then
		echo "Could not determine '$name' version." >&2
		return 1
	fi
	if ! tool::version_ge "$have" "$min_version"; then
		echo "'$name' $have is too old (need >= $min_version): $url" >&2
		return 1
	fi
}

# This section will make sure you don't run into issues from insufficient resources
# and have needed installed base software

function sys::check() {
	local require_kind="${1:-true}"
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
	if $require_kind && ! tool::require_min_version kind "$MIN_KIND_VERSION" "https://kind.sigs.k8s.io/"; then
		fail=true
	fi
	if ! tool::require_min_version skaffold "$MIN_SKAFFOLD_VERSION" "https://skaffold.dev/"; then
		fail=true
	fi
	if ! command -v kubectl >/dev/null 2>&1; then
		echo "'kubectl' is recommended: https://kubernetes.io/docs/reference/kubectl/"
	fi
	if [[ $OSTYPE == 'linux'* ]]; then
		if [ "$(/usr/sbin/sysctl -n kernel.keys.maxkeys)" -lt 2000 ]; then
			echo "Recommended to increase 'kernel.keys.maxkeys':"
			echo "  $ sudo sysctl -w kernel.keys.maxkeys=2000"
			echo "  $ echo 'kernel.keys.maxkeys=2000' | sudo tee --append /etc/sysctl.d/kernel"
		fi
		if [ "$(/usr/sbin/sysctl -n fs.file-max)" -lt 10000000 ]; then
			echo "Recommended to increase 'fs.file-max':"
			echo "  $ sudo sysctl -w fs.file-max=10000000"
			echo "  $ echo 'fs.file-max=10000000' | sudo tee --append /etc/sysctl.d/fs"
		fi
		if [ "$(/usr/sbin/sysctl -n fs.inotify.max_user_instances)" -lt 65535 ]; then
			echo "Recommended to increase 'fs.inotify.max_user_instances':"
			echo "  $ sudo sysctl -w fs.inotify.max_user_instances=65535"
			echo "  $ echo 'fs.inotify.max_user_instances=65535' | sudo tee --append /etc/sysctl.d/fs"
		fi
		if [ "$(/usr/sbin/sysctl -n fs.inotify.max_user_watches)" -lt 1048576 ]; then
			echo "Recommended to increase 'fs.inotify.max_user_watches':"
			echo "  $ sudo sysctl -w fs.inotify.max_user_watches=1048576"
			echo "  $ echo 'fs.inotify.max_user_watches=1048576' | sudo tee --append /etc/sysctl.d/fs"
		fi
	fi

	if $fail; then
		exit 1
	fi
}

function kind::start() {
	sys::check
	local cluster_name="${1:-"kind"}"
	local kind_config="${2:-"$SCRIPT_DIR/kind-config.yaml"}"
	if ! kind get clusters 2>/dev/null | grep -Fxq "$cluster_name"; then
		kind create cluster --name "$cluster_name" --config "$kind_config"
	fi
	kubectl config use-context kind-"$cluster_name"
	slurm-stack::check_node_mode "$OPT_SLURM_NODE_MODE"
	kubectl cluster-info --context kind-"$cluster_name"
}

function kind::delete() {
	local cluster_name="${1:-kind}"
	kind delete cluster --name "$cluster_name"
}

function cluster::use_existing() {
	sys::check false
	echo "[cluster] Using current kubectl context: $(kubectl config current-context)"
	if [ -z "$OPT_REGISTRY" ]; then
		echo "[cluster] WARNING: no --registry or SKAFFOLD_DEFAULT_REPO was provided; local images will only be available if Skaffold can load them into a kind context." >&2
	fi
	slurm-stack::check_node_mode "$OPT_SLURM_NODE_MODE"
	kubectl cluster-info
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

function slurm-stack::installed_node_mode() {
	if ! helm::find slurm; then
		return 0
	fi

	if kubectl get nodeset slurm-worker-slurm-bridge -n slurm >/dev/null 2>&1; then
		echo "$SLURM_NODE_MODE_HYBRID"
		return 0
	fi

	echo "$SLURM_NODE_MODE_EXTERNAL"
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
	echo "[slurm]   $(basename "$0") --recreate --slurm-node-mode=$mode" >&2
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
		local cached_repo
		cached_repo="$(git -C "$path" remote get-url origin 2>/dev/null || true)"
		if [ "$cached_repo" != "$repo" ]; then
			echo "[git] Cached ${name} checkout at ${path} uses a different origin." >&2
			echo "[git]   cached: ${cached_repo:-<none>}" >&2
			echo "[git]   requested: ${repo}" >&2
			echo "[git] Remove the cached checkout and retry:" >&2
			echo "[git]   rm -rf ${path}" >&2
			exit 1
		fi
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
	slurm-bridge::nodes
	echo "[slurm-bridge] Running skaffold (build and deploy slurm-bridge)..."
	(cd "$ROOT_DIR/helm/slurm-bridge" && skaffold run)
}

function slurm-bridge::prerequisites() {
	scheduler-plugins::install
	jobset::install
	lws::install
	storage::install_default_local_path

	echo "[slurm-bridge] Installing slurm (operator + slurm chart)..."
	slurm-stack::install
	echo "[slurm-bridge] Creating slurm-bridge secret and namespace..."
	slurm-bridge::secret
	kubectl create namespace slurm-bridge || true
}

function slurm-bridge::nodes() {

	# Wait for slurm-controller-0 to be ready and give the pod
	# additional time to wait out a reconfigure restart.
	kubectl wait --for=condition=Ready -n slurm pod/slurm-controller-0 --timeout=120s
	sleep 10

	local partition="slurm-bridge"
	if ! kubectl exec -n slurm pods/slurm-controller-0 -- scontrol show partition=$partition >/dev/null 2>&1; then
		kubectl exec -n slurm pods/slurm-controller-0 -- \
			scontrol create partition="$partition"
	fi
	if [ "$OPT_SLURM_NODE_MODE" = "$SLURM_NODE_MODE_EXTERNAL" ]; then
		local bridge_nodes
		bridge_nodes=$(kubectl get nodes -l scheduler.slinky.slurm.net/slurm-bridge=worker \
			-o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
		echo "$bridge_nodes" | while IFS= read -r node; do
			local cpus memory
			cpus=$(kubectl get node "$node" -o jsonpath='{.status.capacity.cpu}')
			memory=$(kubectl get node "$node" -o jsonpath='{.status.capacity.memory}')
			if ! kubectl exec -n slurm pods/slurm-controller-0 -- scontrol show node="$node" >/dev/null 2>&1; then
				kubectl exec -n slurm pods/slurm-controller-0 -- \
					scontrol create nodename="$node" state=external \
					cpus="$cpus" realmemory="${memory%Ki}" \
					gres="gpu:gpu.example.com:8" \
					gresconf=count=8,name=gpu,type=gpu.example.com,file=/home/dev/gpu0
			fi
		done
		kubectl exec -n slurm pods/slurm-controller-0 -- \
			scontrol update partitionname="$partition" nodes="$(echo "$bridge_nodes" | paste -sd, -)"
	else
		kubectl get pods -n slurm -l nodeset.slinky.slurm.net/name=slurm-worker-slurm-bridge \
			-o jsonpath="{range .items[*]}{.spec.nodeName} {.spec.hostname}{'\n'}{end}" | while read -r node hostname; do
			if [[ -n $node && -n $hostname ]]; then
				kubectl label node "$node" slinky.slurm.net/slurm-nodename="$hostname"
			else
				echo "Skipping node as one or both of 'node'/'hostname' is not set" >&2
			fi
		done
	fi
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
		local version="v0.8.1"
		helm install "$chartName" oci://registry.k8s.io/jobset/charts/jobset \
			--version "$version" --namespace "${chartName}-system" --create-namespace
	fi
}

function lws::install() {
	local chartName
	chartName="lws"
	if ! helm::find "$chartName"; then
		echo "[slurm-bridge] Installing lws (LeaderWorkerSet)..."
		local version="v0.6.2"
		helm install "$chartName" \
			"https://github.com/kubernetes-sigs/lws/releases/download/${version}/lws-chart-${version}.tgz" \
			--namespace "${chartName}-system" --create-namespace
	fi
}

function storage::has_default_class() {
	kubectl get storageclass \
		-o go-template='{{range .items}}{{if or (eq (index .metadata.annotations "storageclass.kubernetes.io/is-default-class") "true") (eq (index .metadata.annotations "storageclass.beta.kubernetes.io/is-default-class") "true")}}{{.metadata.name}}{{"\n"}}{{end}}{{end}}' 2>/dev/null |
		grep -q .
}

function storage::install_default_local_path() {
	if storage::has_default_class; then
		echo "[storage] Default StorageClass already exists."
		return
	fi

	echo "[storage] No default StorageClass found; installing local-path provisioner..."
	helm upgrade --install local-path-provisioner "$LOCAL_PATH_PROVISIONER_CHART" \
		--version "$LOCAL_PATH_PROVISIONER_VERSION" \
		--namespace local-path-storage --create-namespace \
		--set storageClass.defaultClass=true \
		--set storageClass.provisionerName=rancher.io/local-path \
		--wait --timeout=120s
	if ! storage::has_default_class; then
		echo "[storage] local-path provisioner installed, but no default StorageClass was found." >&2
		exit 1
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
	local repo="$OPT_SLURM_OPERATOR_REPO"

	slurm-stack::prerequisites

	operator_path="$(git::checkout slurm-operator "$repo" "$ref")"
	make -C "$operator_path" values-dev
	slurm-operator-crds::install_from_source "$operator_path"
	slurm-operator::install_from_source "$operator_path"
	slurm::install_from_source "$operator_path"

	slurm::configure_for_bridge "$operator_path/helm/slurm"
}

function slurm-operator-crds::install_from_source() {
	local operator_path="$1"

	echo "[slurm] Installing slurm-operator CRDs..."
	(
		cd "$operator_path/helm/slurm-operator-crds"
		skaffold run
	)
}

function slurm-operator::install_from_source() {
	local operator_path="$1"

	echo "[slurm] Installing slurm-operator..."
	(
		cd "$operator_path/helm/slurm-operator"
		skaffold run -p dev
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
	case "$OPT_SLURM_NODE_MODE" in
	"$SLURM_NODE_MODE_EXTERNAL")
		helm upgrade "$chartName" "$chart" \
			--namespace slurm --create-namespace \
			--reuse-values \
			--wait \
			--values "$SCRIPT_DIR/slurm-bridge-external.yaml"
		;;
	"$SLURM_NODE_MODE_HYBRID")
		helm upgrade "$chartName" "$chart" \
			--namespace slurm --create-namespace \
			--reuse-values \
			--wait \
			--values "$SCRIPT_DIR/slurm-bridge-hybrid.yaml"
		;;
	*)
		echo "[slurm] Unsupported slurm node mode: $OPT_SLURM_NODE_MODE" >&2
		exit 1
		;;
	esac
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
	kubectl apply -f "${SCRIPT_DIR}"/kjob.yaml
	echo -e "\nRun the following command to install the kubectl kjob plugin:"
	echo -e "sudo cp ${SCRIPT_DIR}/kubectl-kjob /usr/local/bin/kubectl-kjob\n"
}

function dra-example-driver::install() {
	local cluster_name="${1:-kind}"
	local version="0.2.0"
	local dra_path
	local repo="https://github.com/kubernetes-sigs/dra-example-driver.git"
	dra_path="$(git::checkout dra-example-driver "$repo" "v${version}")"
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

function main::help() {
	cat <<EOF
$(basename "$0") - Manage a kind cluster for a slurm-bridge slurm-bridge-demo

	usage: $(basename "$0") [--config=KIND_CONFIG_PATH] [--existing-cluster]
	        [--recreate|--delete]
	        [--core|--prereqs][--extras][--all] [--registry=REPO]
	        [--kjob] [--dra-example-driver] [--dra-driver-cpu]
	        [--slurm-node-mode=MODE]
	        [--slurm-operator-repo=URL] [--slurm-operator-ref=REF]
	        [-h|--help] [--debug] [KIND_CLUSTER_NAME]

KIND OPTIONS:
	--config=PATH       Use the specified kind config when creating.
	--existing-cluster  Use the current kubectl context instead of creating or switching to a kind cluster.
	--registry=REPO     Push locally built images to REPO with Skaffold before deploying.
	                    Can also be set with SKAFFOLD_DEFAULT_REPO.
	--recreate          Delete the Kind cluster and continue.
	--delete            Delete the Kind cluster and exit.

HELM OPTIONS:
	--all               Equivalent of: --core --extras
	--extras            Equivalent of: --dra-example-driver
	--core              Install the slurm-bridge stack.
	--prereqs           Install slurm-bridge prerequisites only.
	--kjob              Install kjob CRDs and build kubectl-kjob
	--dra-driver-cpu    Unsupported in release-1.0.
	--dra-example-driver Install DRA driver: dra-example-driver

SLURM OPTIONS:
	--slurm-node-mode=MODE
	                    Configure Slurm nodes as external or hybrid. Default: $OPT_SLURM_NODE_MODE.
	--slurm-operator-repo=URL
	                    Clone slurm-operator from URL. Default: $OPT_SLURM_OPERATOR_REPO.
	                    Can also be set with SLURM_OPERATOR_REPO.
	--slurm-operator-ref=REF
	                    Clone slurm-operator from REF. Default: $OPT_SLURM_OPERATOR_REF.

HELP OPTIONS:
	--debug             Show script debug information.
	-h, --help          Show this help message.

EOF
}

function main::validate_options() {
	if $OPT_EXISTING_CLUSTER && { $OPT_DELETE || $OPT_RECREATE; }; then
		echo "--existing-cluster cannot be used with --delete or --recreate." >&2
		exit 1
	fi
	if $OPT_DRA_DRIVER_CPU; then
		echo "--dra-driver-cpu is not supported in release-1.0." >&2
		exit 1
	fi
	if $OPT_EXISTING_CLUSTER && $OPT_DRA_EXAMPLE_DRIVER; then
		echo "--existing-cluster cannot be used with kind-specific DRA demo installers." >&2
		exit 1
	fi
	if $OPT_CORE && $OPT_PREREQS; then
		echo "--core and --prereqs cannot be used together." >&2
		exit 1
	fi
}

function main() {
	if $OPT_DEBUG; then
		set -x
	fi
	main::validate_options
	local cluster_name="${1:-"kind"}"
	if $OPT_DELETE || $OPT_RECREATE; then
		kind::delete "$cluster_name"
		$OPT_DELETE && return
	fi

	if $OPT_EXISTING_CLUSTER; then
		cluster::use_existing
	else
		kind::start "$cluster_name" "$OPT_CONFIG"
	fi

	make -C "$ROOT_DIR" values-dev || true

	if $OPT_DRA_EXAMPLE_DRIVER; then
		dra-example-driver::install "$cluster_name"
	fi
	if $OPT_PREREQS; then
		slurm-bridge::prerequisites
	elif $OPT_CORE; then
		slurm-bridge::install
	fi
	if $OPT_KJOB; then
		kjob::install
	fi
}

OPT_DEBUG=false
OPT_RECREATE=false
OPT_CONFIG="$SCRIPT_DIR/kind-config.yaml"
OPT_DELETE=false
OPT_EXISTING_CLUSTER=false
OPT_CORE=false
OPT_PREREQS=false
OPT_REGISTRY="${SKAFFOLD_DEFAULT_REPO:-}"
OPT_EXTRAS=false
OPT_DRA_DRIVER_CPU=false
OPT_DRA_EXAMPLE_DRIVER=false
OPT_KJOB=false
OPT_SLURM_OPERATOR_REPO="${SLURM_OPERATOR_REPO:-https://github.com/SlinkyProject/slurm-operator.git}"
OPT_SLURM_OPERATOR_REF="release-1.0"
OPT_SLURM_NODE_MODE="$SLURM_NODE_MODE_EXTERNAL"

SHORT="+h"
LONG="all,recreate,config:,delete,debug,existing-cluster,registry:,core,prereqs,extras,kjob,dra-driver-cpu,dra-example-driver,slurm-operator-repo:,slurm-operator-ref:,slurm-node-mode:,help"
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
	--existing-cluster)
		OPT_EXISTING_CLUSTER=true
		shift
		;;
	--registry)
		OPT_REGISTRY="$2"
		if [ -z "$OPT_REGISTRY" ]; then
			echo "--registry requires a non-empty REPO" >&2
			exit 1
		fi
		export SKAFFOLD_DEFAULT_REPO="$OPT_REGISTRY"
		shift 2
		;;
	--core)
		OPT_CORE=true
		shift
		;;
	--prereqs)
		OPT_PREREQS=true
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
	--slurm-operator-repo)
		OPT_SLURM_OPERATOR_REPO="$2"
		if [ -z "$OPT_SLURM_OPERATOR_REPO" ]; then
			echo "--slurm-operator-repo requires a non-empty URL" >&2
			exit 1
		fi
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
		OPT_CORE=true
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

if $OPT_EXTRAS; then
	OPT_DRA_EXAMPLE_DRIVER=true
fi

main "$@"
