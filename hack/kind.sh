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
KWOK_CHART_REPO="https://kwok.sigs.k8s.io/charts/"
KWOK_CHART_VERSION="0.3.0"
KUBE_PROMETHEUS_STACK_CHART_REPO="https://prometheus-community.github.io/helm-charts"
KUBE_PROMETHEUS_STACK_CHART_VERSION="88.6.2"

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
	local cluster_name="${1:-"kind"}"
	local kind_config="${2:-"$SCRIPT_DIR/kind.yaml"}"
	if ! kind get clusters 2>/dev/null | grep -Fxq "$cluster_name"; then
		kind create cluster --name "$cluster_name" --config "$kind_config"
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

function cluster::use_existing() {
	sys::check false
	echo "[cluster] Using current kubectl context: $(kubectl config current-context)"
	if [ -z "$OPT_REGISTRY" ]; then
		echo "[cluster] WARNING: no --registry or SKAFFOLD_DEFAULT_REPO was provided; local images will only be available if Skaffold can load them into a kind context." >&2
	fi
	if $OPT_CORE || $OPT_PREREQS; then
		slurm-stack::check_node_mode "$OPT_SLURM_NODE_MODE"
	fi
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

function kind::configure_nodes() {
	local mode="$1"

	if [ "$mode" = "$SLURM_NODE_MODE_EXTERNAL" ]; then
		kubectl label nodes -l scheduler.slinky.slurm.net/slurm-bridge=worker \
			scheduler.slinky.slurm.net/external-node=true --overwrite
		# Annotate external nodes with partition list (Kind node config does not support annotations).
		kubectl annotate nodes -l scheduler.slinky.slurm.net/external-node=true \
			scheduler.slinky.slurm.net/external-node-partitions=slurm-bridge --overwrite
	fi

	local bridge_nodes
	local bridge_node
	local node_index=0
	bridge_nodes="$(kubectl get nodes -l scheduler.slinky.slurm.net/slurm-bridge=worker -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)"
	for bridge_node in $bridge_nodes; do
		node_index=$((node_index + 1))
		if [ "$node_index" -le 2 ]; then
			kubectl annotate node "$bridge_node" \
				topology.slinky.slurm.net/spec=topo-switch:s1 --overwrite
		else
			kubectl annotate node "$bridge_node" \
				topology.slinky.slurm.net/spec=topo-switch:s2 --overwrite
		fi
	done
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
	storage::install_default_local_path

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
		local version="0.12.0"
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

function kwok::install() {
	echo "[kwok] Installing the KWOK controller and fast stage configuration..."
	helm repo add kwok "$KWOK_CHART_REPO" --force-update
	helm upgrade --install kwok kwok/kwok \
		--version "$KWOK_CHART_VERSION" \
		--namespace kube-system --create-namespace \
		--set hostNetwork=true \
		--wait --timeout=120s
	helm upgrade --install kwok-stage-fast kwok/stage-fast \
		--version "$KWOK_CHART_VERSION" \
		--namespace kube-system --create-namespace \
		--wait --timeout=120s
	kubectl rollout status deployment/kwok-controller \
		--namespace kube-system --timeout=120s
}

function metrics::install() {
	local config_dir="$SCRIPT_DIR/metrics"

	echo "[metrics] Installing kube-prometheus-stack..."
	helm repo add prometheus-community "$KUBE_PROMETHEUS_STACK_CHART_REPO" --force-update
	helm upgrade --install prometheus prometheus-community/kube-prometheus-stack \
		--version "$KUBE_PROMETHEUS_STACK_CHART_VERSION" \
		--namespace monitoring --create-namespace \
		--values "$config_dir/values.yaml" \
		--wait --timeout=300s
	kubectl apply -f "$config_dir/slurm-bridge.yaml"
	kubectl wait --for=create pod \
		--namespace monitoring \
		--selector=app.kubernetes.io/name=prometheus \
		--timeout=120s
	kubectl wait --for=condition=Ready pod \
		--namespace monitoring \
		--selector=app.kubernetes.io/name=prometheus \
		--timeout=300s
	echo "[metrics] Ready. Forward the Prometheus UI with:"
	echo "kubectl --namespace monitoring port-forward service/prometheus-kube-prometheus-prometheus 9090:9090"
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
		slurm::configure_hybrid_dra_inventory
		;;
	*)
		echo "[slurm] Unsupported slurm node mode: $OPT_SLURM_NODE_MODE" >&2
		exit 1
		;;
	esac
}

function slurm::configure_hybrid_dra_inventory() {
	local bridge_nodes
	local desired_nodes
	local example_devices
	local index
	local nvidia_devices
	local node
	local extra

	bridge_nodes="$(kubectl get nodes -l scheduler.slinky.slurm.net/slurm-bridge=worker \
		-o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)"
	desired_nodes="$(printf '%s\n' "$bridge_nodes" | sed '/^$/d' | wc -l | tr -d ' ')"
	if [ "$desired_nodes" -eq 0 ]; then
		echo "[slurm] No hybrid worker nodes found." >&2
		exit 1
	fi

	kubectl wait nodeset/slurm-worker-slurm-bridge -n slurm \
		--for=jsonpath='{.status.readyReplicas}'="$desired_nodes" --timeout=180s

	for node in $bridge_nodes; do
		example_devices=""
		for index in 0 1 2 3; do
			example_devices="${example_devices}${example_devices:+,}\"/dra/gpu.example.com/$node/gpu-$index\""
		done
		nvidia_devices=""
		for index in 0 1 2 3 4 5 6 7; do
			nvidia_devices="${nvidia_devices}${nvidia_devices:+,}\"/dra/gpu.nvidia.com/$node/gpu-$index\""
		done
		extra="slurm-bridge.dra-gres-map={\"v\":1,\"profiles\":{\"gpu-example\":[$example_devices],\"gpu-nvidia\":[$nvidia_devices]}}"
		kubectl exec -n slurm slurm-controller-0 -- \
			scontrol update NodeName="$node" "Extra=$extra"
	done
}

function slurm-bridge::secret() {
	kubectl apply -f "${SCRIPT_DIR}"/token.yaml
}

function dra-example-driver::install() {
	local version="0.4.0"
	local chart="oci://registry.k8s.io/dra-example-driver/charts/dra-example-driver"
	local values="$SLURM_BRIDGE_TMP/dra-example-driver-values.yaml"

	cat <<EOF >"$values"
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

	helm upgrade --install dra-example-driver "$chart" \
		--version "$version" \
		--namespace dra-example-driver \
		--create-namespace \
		--values "$values" \
		--wait --timeout=120s
}

function dra-driver-cpu::install() {
	local version="0.2.0"
	local chart="https://github.com/kubernetes-sigs/dra-driver-cpu/releases/download/v${version}/dra-driver-cpu-${version}.tgz"
	local config_dir="$SCRIPT_DIR/dra-driver-cpu"

	helm upgrade --install dra-driver-cpu "$chart" \
		--namespace kube-system \
		--values "$config_dir/values.yaml"

	# The upstream v0.2.0 chart does not expose a nodeSelector value.
	kubectl -n kube-system patch daemonset dracpu --type merge \
		-p '{"spec":{"template":{"spec":{"nodeSelector":{"scheduler.slinky.slurm.net/slurm-bridge":"worker"}}}}}'
	kubectl -n kube-system rollout status daemonset/dracpu --timeout=120s
}

function dra-driver-nvidia-gpu::install() {
	local version="0.5.0"
	local chart="oci://registry.k8s.io/dra-driver-nvidia/charts/dra-driver-nvidia-gpu"
	local config_dir="$SCRIPT_DIR/dra-driver-nvidia-gpu"
	local values_args=(--values "$config_dir/values.yaml")

	if $MOCK_NVML; then
		values_args+=(--values "$config_dir/mock-values.yaml")
	fi

	helm upgrade --install dra-driver-nvidia-gpu "$chart" \
		--version "$version" \
		--namespace dra-driver-nvidia-gpu \
		--create-namespace \
		"${values_args[@]}" \
		--wait --timeout=180s
}

function nvml-mock::install() {
	local version="0.3.0"
	local chart="oci://ghcr.io/nvidia/k8s-test-infra/chart/nvml-mock"
	local config_dir="$SCRIPT_DIR/nvml-mock"

	helm upgrade --install nvml-mock "$chart" \
		--version "$version" \
		--namespace nvml-mock \
		--create-namespace \
		--values "$config_dir/values.yaml" \
		--wait --timeout=180s
}

function nvml-mock::uninstall() {
	helm uninstall nvml-mock \
		--namespace nvml-mock \
		--ignore-not-found \
		--wait --timeout=180s
}

function main::help() {
	cat <<EOF
$(basename "$0") - Manage a kind cluster for a slurm-bridge slurm-bridge-demo

	usage: $(basename "$0") [--config=KIND_CONFIG_PATH] [--existing-cluster]
	        [--recreate|--delete]
	        [--core|--prereqs][--extras][--all] [--registry=REPO]
	        [--dra-example-driver] [--dra-driver-cpu]
	        [--dra-driver-nvidia-gpu] [--kwok] [--metrics]
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
	--extras            Install all DRA driver fixtures below.
	--core              Install the slurm-bridge stack.
	--prereqs           Install slurm-bridge prerequisites only.
	--dra-driver-cpu    Install DRA driver: dra-driver-cpu
	--dra-example-driver Install DRA driver: dra-example-driver
	--dra-driver-nvidia-gpu Install DRA driver: dra-driver-nvidia-gpu
	                    Set MOCK_NVML=true to expose fake GPUs on Kind workers.
	--kwok              Install KWOK and its fast stage configuration.
	--metrics           Install metrics collection for Slurm Bridge.

SLURM OPTIONS:
	--slurm-node-mode=MODE
	                    Configure Slurm nodes as external or hybrid. Default: $OPT_SLURM_NODE_MODE.
	--slurm-operator-repo=URL
	                    Clone slurm-operator from URL. Default: $OPT_SLURM_OPERATOR_REPO.
	                    Can also be set with SLURM_OPERATOR_REPO.
	--slurm-operator-ref=REF
	                    Clone slurm-operator from REF. Default: $OPT_SLURM_OPERATOR_REF.
	                    Can also be set with SLURM_OPERATOR_REF.

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

	if $OPT_KWOK; then
		kwok::install
	fi
	if $OPT_DRA_DRIVER_CPU; then
		dra-driver-cpu::install
	fi
	if $OPT_DRA_EXAMPLE_DRIVER; then
		dra-example-driver::install
	fi
	if $OPT_DRA_DRIVER_NVIDIA_GPU; then
		if $MOCK_NVML; then
			nvml-mock::install
		else
			nvml-mock::uninstall
		fi
		dra-driver-nvidia-gpu::install
	fi
	if $OPT_PREREQS; then
		slurm-bridge::prerequisites
	elif $OPT_CORE; then
		slurm-bridge::install
	fi
	if $OPT_METRICS; then
		metrics::install
	fi
}

OPT_DEBUG=false
OPT_RECREATE=false
OPT_CONFIG="$SCRIPT_DIR/kind.yaml"
OPT_DELETE=false
OPT_EXISTING_CLUSTER=false
OPT_CORE=false
OPT_PREREQS=false
OPT_REGISTRY="${SKAFFOLD_DEFAULT_REPO:-}"
OPT_EXTRAS=false
OPT_DRA_DRIVER_CPU=false
OPT_DRA_EXAMPLE_DRIVER=false
OPT_DRA_DRIVER_NVIDIA_GPU=false
MOCK_NVML="${MOCK_NVML:-false}"
OPT_KWOK=false
OPT_METRICS=false
OPT_SLURM_OPERATOR_REPO="${SLURM_OPERATOR_REPO:-https://github.com/SlinkyProject/slurm-operator.git}"
OPT_SLURM_OPERATOR_REF="${SLURM_OPERATOR_REF:-main}"
OPT_SLURM_NODE_MODE="$SLURM_NODE_MODE_EXTERNAL"

case "$MOCK_NVML" in
true | false) ;;
*)
	echo "MOCK_NVML must be either true or false" >&2
	exit 1
	;;
esac

SHORT="+h"
LONG="all,recreate,config:,delete,debug,existing-cluster,registry:,core,prereqs,extras,dra-driver-cpu,dra-example-driver,dra-driver-nvidia-gpu,kwok,metrics,slurm-operator-repo:,slurm-operator-ref:,slurm-node-mode:,help"
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
	--dra-driver-cpu)
		OPT_DRA_DRIVER_CPU=true
		shift
		;;
	--dra-example-driver)
		OPT_DRA_EXAMPLE_DRIVER=true
		shift
		;;
	--dra-driver-nvidia-gpu)
		OPT_DRA_DRIVER_NVIDIA_GPU=true
		shift
		;;
	--kwok)
		OPT_KWOK=true
		shift
		;;
	--metrics)
		OPT_METRICS=true
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
	OPT_DRA_DRIVER_CPU=true
	OPT_DRA_EXAMPLE_DRIVER=true
	OPT_DRA_DRIVER_NVIDIA_GPU=true
fi

main "$@"
