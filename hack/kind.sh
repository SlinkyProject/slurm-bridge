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
	if ! command -v yq >/dev/null 2>&1; then
		echo "'yq' is required: https://github.com/mikefarah/yq"
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
	kind::prerequisites
	local cluster_name="${1:-"kind"}"
	local kind_config="${2:-"$SCRIPT_DIR/kind-config.yaml"}"
	if [ "$(kind get clusters | grep -oc kind)" -eq 0 ]; then
		if [ "$(command -v systemd-run)" ]; then
			CMD="systemd-run --scope --user"
		else
			CMD=""
		fi
		$CMD kind create cluster --name "$cluster_name" --config "$kind_config"
	fi
	kubectl cluster-info --context kind-"$cluster_name"
}

function kind::delete() {
	kind delete cluster --name "$cluster_name"
}

function pre::install() {
	slurm-bridge::prerequisites
}

function helm::uninstall() {
	local namespace=(
		"scheduler-plugins"
		"slurm"
		"slurm-operator"
		"slinky"
		"cert-manager"
		"jobset"
	)
	if $FLAG_EXTRAS; then
		namespace+=("metrics-server")
		namespace+=("prometheus")
		namespace+=("keda")
	fi
	for name in "${namespace[@]}"; do
		if [ "$(helm --namespace="$name" list --all --short | wc -l)" -gt 0 ]; then
			helm uninstall --namespace="$name" "$(helm --namespace="$name" ls --all --short)"
		fi
	done
}

function slurm-bridge::helm() {
	slurm-bridge::prerequisites

	local slurm_values_yaml="$ROOT_DIR/helm/slurm-bridge/values-dev.yaml"
	if [ ! -f "$slurm_values_yaml" ]; then
		echo "ERROR: Missing values file: $slurm_values_yaml"
		exit 1
	fi
	local helm_release="slurm-bridge"
	if [ "$(helm list --all-namespaces --short --filter="$helm_release" | wc -l)" -eq 0 ]; then
		helm install "$helm_release" "$ROOT_DIR"/helm/slurm-bridge/ -f "$slurm_values_yaml"
	else
		echo "WARNING: helm release '$helm_release' exists. Skipping."
	fi
}

function slurm-bridge::skaffold() {
	slurm-bridge::prerequisites
	(
		make values-dev || true
		cd "$ROOT_DIR/helm/slurm-bridge"
		skaffold run -p dev
	)
}

function slurm-bridge::prerequisites() {
	helm repo add bitnami https://charts.bitnami.com/bitnami
	helm repo add jetstack https://charts.jetstack.io
	if $FLAG_EXTRAS; then
		helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
		helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
		helm repo add kedacore https://kedacore.github.io/charts
	fi
	helm repo update
	local certManager="cert-manager"
	if [ "$(helm list --all-namespaces --short --filter="$certManager" | wc -l)" -eq 0 ]; then
		helm install "$certManager" jetstack/cert-manager \
			--namespace "$certManager" --create-namespace --set crds.enabled=true
	fi
	# enables podgroup
	local schedPlugins="scheduler-plugins"
	if [ "$(helm list --all-namespaces --short --filter="$schedPlugins" | wc -l)" -eq 0 ]; then
		helm install --repo https://scheduler-plugins.sigs.k8s.io "$schedPlugins" "$schedPlugins" \
			--namespace "$schedPlugins" --create-namespace \
			--set 'plugins.enabled={CoScheduling}' --set 'scheduler.replicaCount=0'
	fi
	local jobset="jobset"
	local jobsetVersion="v0.8.1"
	local jobsetNamespace="jobset-system"
	if [ "$(helm list --all-namespaces --short --filter="$jobset" | wc -l)" -eq 0 ]; then
		helm install "$jobset" oci://registry.k8s.io/jobset/charts/jobset --version "$jobsetVersion" \
			--namespace "$jobsetNamespace" --create-namespace
	fi
	if $FLAG_EXTRAS; then
		local metrics="metrics-server"
		if [ "$(helm list --all-namespaces --short --filter="$metrics" | wc -l)" -eq 0 ]; then
			helm install "$metrics" metrics-server/metrics-server \
				--set args="{--kubelet-insecure-tls}" \
				--namespace "$metrics" --create-namespace
		fi
		local prometheus="prometheus"
		if [ "$(helm list --all-namespaces --short --filter="$prometheus" | wc -l)" -eq 0 ]; then
			helm install "$prometheus" prometheus-community/kube-prometheus-stack \
				--namespace "$prometheus" --create-namespace --set installCRDs=true \
				--set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false
		fi
		local keda="keda"
		if [ "$(helm list --all-namespaces --short --filter="$keda" | wc -l)" -eq 0 ]; then
			helm install "$keda" kedacore/keda \
				--namespace "$keda" --create-namespace
		fi
	fi
	slurm::install
	slurm-bridge::secret
	kubectl create namespace slurm-bridge || true
}

function slurm::install() {
	local version="0.3.0"

	local slurmOperator="slurm-operator"
	if [ "$(helm list --all-namespaces --short --filter="^${slurmOperator}$" | wc -l)" -eq 0 ]; then
		helm install $slurmOperator oci://ghcr.io/slinkyproject/charts/slurm-operator \
			--version="$version" --namespace=slinky --create-namespace --wait
	fi

	local slurm="slurm"
	if [ "$(helm list --all-namespaces --short --filter="^${slurm}$" | wc -l)" -eq 0 ]; then
		local valuesFile="/tmp/values-$slurm.yaml"
		curl -L "https://raw.githubusercontent.com/SlinkyProject/slurm-operator/refs/tags/v${version}/helm/$slurm/values.yaml" -o "$valuesFile"
		yq -i '.compute.nodesets[0].name = "slurm-bridge"' "$valuesFile"
		yq -i '.compute.nodesets[0].replicas = 3' "$valuesFile"
		helm install slurm oci://ghcr.io/slinkyproject/charts/slurm \
			--version="$version" --namespace=slurm --create-namespace --wait \
			-f "$valuesFile"
	fi
}

function slurm-bridge::secret() {
	kubectl delete secret slurm-bridge-jwt-token --namespace=slinky || true
	SLURM_JWT="$(kubectl --namespace=slurm get secrets slurm-token-slurm -o jsonpath="{.data.auth-token}" | base64 --decode)"
	export SLURM_JWT
	kubectl create secret generic slurm-bridge-jwt-token --namespace=slinky --from-literal="auth-token=$SLURM_JWT" --type=Opaque
}

function kjob::install() {
	local version="0.1.0"
	local kjob_path="/tmp/kjob/"
	mkdir -p ${kjob_path}
	curl -L "https://github.com/kubernetes-sigs/kjob/archive/refs/tags/v${version}.tar.gz" -o $kjob_path/kjob-${version}.tar.gz
	(
		cd $kjob_path
		tar xzf kjob-${version}.tar.gz
		cd kjob-${version}
		make install
		make kubectl-kjob
	)
	kubectl apply -f ${SCRIPT_DIR}/kjob.yaml
	cp $kjob_path/kjob-${version}/bin/kubectl-kjob "$SCRIPT_DIR/kubectl-kjob"
	echo -e "\nRun the following command to install the kubectl kjob plugin:"
	echo -e "sudo cp /tmp/kjob/kjob-${version}/bin/kubectl-kjob /usr/local/bin/kubectl-kjob\n"
}

function main::help() {
	cat <<EOF
$(basename "$0") - Manage a kind cluster for a slurm-bridge slurm-bridge-demo

	usage: $(basename "$0") [--create|--delete] [--config=KIND_CONFIG_PATH]
	        [--install|--uninstall] [--extras] [--kjob]
	        [-h|--help] [KIND_CLUSTER_NAME]

ONESHOT OPTIONS:
	--create            Create kind cluster and nothing else.
	--delete            Delete kind cluster and nothing else.
	--install           Install dependencies and nothing else.
	--uninstall         Uninstall all helm releases and nothing else.

OPTIONS:
	--config=PATH       Use the specified kind config when creating.
	--bridge            Deploy slurm-bridge with skaffold.
    --extras            Install optional dependencies (metrics, prometheus, keda).
    --kjob              Install kjob CRDs and build kubectl-kjob

HELP OPTIONS:
	--debug             Show script debug information.
	-h, --help          Show this help message.

EOF
}

function main() {
	if $FLAG_DEBUG; then
		set -x
	fi
	local cluster_name="${1:-"kind"}"
	if $FLAG_DELETE; then
		kind::delete "$cluster_name"
		return
	elif $FLAG_UNINSTALL; then
		helm::uninstall
		return
	elif $FLAG_CREATE; then
		kind::start "$cluster_name" "$FLAG_CONFIG"
		return
	fi

	kind::start "$cluster_name" "$FLAG_CONFIG"

	if $FLAG_INSTALL; then
		pre::install
		return
	fi
	if $FLAG_BRIDGE; then
		if $FLAG_HELM; then
			slurm-bridge::helm
		else
			slurm-bridge::skaffold
		fi
	fi
	if $FLAG_KJOB; then
		kjob::install
	fi
}

FLAG_DEBUG=false
FLAG_CREATE=false
FLAG_CONFIG="$SCRIPT_DIR/kind-config.yaml"
FLAG_DELETE=false
FLAG_HELM=false
FLAG_INSTALL=false
FLAG_UNINSTALL=false
FLAG_BRIDGE=false
FLAG_EXTRAS=false
FLAG_KJOB=false

SHORT="+h"
LONG="create,config:,delete,debug,helm,bridge,install,extras,kjob,uninstall,help"
OPTS="$(getopt -a --options "$SHORT" --longoptions "$LONG" -- "$@")"
eval set -- "${OPTS}"
while :; do
	case "$1" in
	--debug)
		FLAG_DEBUG=true
		shift
		;;
	--create)
		FLAG_CREATE=true
		shift
		if $FLAG_CREATE && $FLAG_DELETE; then
			echo "Flags --create and --delete are mutually exclusive!"
			exit 1
		fi
		;;
	--config)
		FLAG_CONFIG="$2"
		shift 2
		;;
	--delete)
		FLAG_DELETE=true
		shift
		if $FLAG_CREATE && $FLAG_DELETE; then
			echo "Flags --create and --delete are mutually exclusive!"
			exit 1
		fi
		;;
	--helm)
		FLAG_HELM=true
		shift
		;;
	--bridge)
		FLAG_BRIDGE=true
		shift
		;;
	--install)
		FLAG_INSTALL=true
		shift
		if $FLAG_INSTALL && $FLAG_UNINSTALL; then
			echo "Flags --install and --uninstall are mutually exclusive!"
			exit 1
		fi
		;;
	--extras)
		FLAG_EXTRAS=true
		shift
		;;
	--kjob)
		FLAG_KJOB=true
		shift
		;;
	--uninstall)
		FLAG_UNINSTALL=true
		shift
		if $FLAG_INSTALL && $FLAG_UNINSTALL; then
			echo "Flags --install and --uninstall are mutually exclusive!"
			exit 1
		fi
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
		log::error "Unknown option: $1"
		exit 1
		;;
	esac
done
main "$@"
