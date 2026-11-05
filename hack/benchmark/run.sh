#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (C) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Run against an explicitly prepared, dedicated benchmark cluster.
set -euo pipefail
cd "$(dirname "$0")/../.."
for input in CONTEXT CONFIG_DIR ARTIFACTS_ROOT SPEEDUP TIMEOUT KNAVIGATOR KNAVIGATOR_VERSION; do
	[[ -n ${!input:-} ]] || {
		echo "error: $input is required; use make benchmark" >&2
		exit 2
	}
done
command -v jq >/dev/null
run_id="${BENCHMARK_RUN_ID:-$(date -u +%Y%m%d-%H%M%S)-${RANDOM}}"
namespace="benchmark-${run_id}"
[[ $namespace =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#namespace} -le 63 ]] || {
	echo 'BENCHMARK_RUN_ID must form a valid Kubernetes namespace' >&2
	exit 2
}
artifacts="${ARTIFACTS_ROOT}/${run_id}"
mkdir -p "$ARTIFACTS_ROOT"
mkdir "$artifacts" # Refuse to overwrite a previous run.
for file in nodes.yaml podgroup.yaml.tmpl kwok-stage.yaml; do
	cp "$CONFIG_DIR/$file" "$artifacts/$file"
done
cp "${WORKLOAD:-$CONFIG_DIR/workload.yaml}" "$artifacts/workload.yaml"
artifacts="$(cd "$artifacts" && pwd)"
exec > >(tee "$artifacts/benchmark.log") 2>&1
echo "Run $run_id on $CONTEXT; artifacts: $artifacts"

export BENCHMARK_NAMESPACE="$namespace"
# Native -var expansion inserts YAML, so quote the absolute template path.
BENCHMARK_TEMPLATE="$(jq -Rn --arg path "$artifacts/podgroup.yaml.tmpl" '$path | tojson' -r)"
export BENCHMARK_TEMPLATE
locked=false
namespace_owned=false
knavigator_pid=""
kube() { kubectl --context "$CONTEXT" --request-timeout=30s "$@"; }
slurm() { kube exec -n slurm slurm-controller-0 -c slurmctld -- "$@"; }
run_workflow() {
	local workflow="$1" entry
	shift
	local args=(-kubectx "$CONTEXT" -workflow "$workflow")
	jq -n --rawfile yaml "$workflow" '
  [$yaml | scan("\\$\\{([A-Za-z_][A-Za-z0-9_]*)") | .[0]] | unique |
  map(. as $name | select(env[$name] != null and env[$name] != "") |
   {key: $name, value: env[$name]}) | from_entries
 ' >"${workflow%.yaml}.vars.json"
	while IFS= read -r -d "" entry; do args+=(-var "$entry"); done < <(
		jq -j 'to_entries[] | .key + "=" + .value + "\u0000"' "${workflow%.yaml}.vars.json"
	)
	exec "$KNAVIGATOR" "${args[@]}" "$@"
}
report_progress() {
	kube get jobs -n "$namespace" -l app.kubernetes.io/managed-by=knavigator -o json >"$artifacts/jobs-live.json" || return
	kube get pods -n "$namespace" -l app.kubernetes.io/managed-by=knavigator -o json >"$artifacts/pods-live.json" || return
	jq -n --argjson elapsed "$((SECONDS - started))" --slurpfile jobs "$artifacts/jobs-live.json" \
		--slurpfile pods "$artifacts/pods-live.json" --slurpfile fleet "$artifacts/fleet.json" '
  def counts: {waiting: map(select(.state == "waiting")) | length,
   active: map(select(.state == "active")) | length, completed: map(select(.state == "completed")) | length};
  def gpus: [.[] | .spec.containers[].resources.requests["nvidia.com/gpu"] // 0 | tonumber] | add // 0;
  [$pods[0].items[] | select((.spec.nodeName // "") != "") | .metadata.ownerReferences[] | select(.kind == "Job") | .uid] |
   group_by(.) | map({key: .[0], value: length}) | from_entries as $bound |
  [$jobs[0].items[] | (.spec.completions // 1) as $size | {size: $size, state:
   (if (.status.succeeded // 0) >= $size then "completed"
    elif ($bound[.metadata.uid] // 0) >= $size then "active" else "waiting" end)}] as $jobs |
  [$pods[0].items[] | select((.spec.nodeName // "") != "" and .status.phase != "Succeeded" and .status.phase != "Failed")] as $occupied |
  ([$fleet[0].items[].status.allocatable["nvidia.com/gpu"] | tonumber] | add) as $capacity |
  ($jobs | counts) + {elapsed_seconds: $elapsed, submitted: ($jobs | length),
   occupied_gpus: ($occupied | gpus), total_gpus: $capacity,
   gpu_occupancy_percent: (if $capacity > 0 then ($occupied | gpus) * 1000 / $capacity | round / 10 else 0 end),
   running_gpus: ([$occupied[] | select(.status.phase == "Running")] | gpus),
   busy_nodes: ([$occupied[].spec.nodeName] | unique | length), total_nodes: ($fleet[0].items | length),
   jobs_by_size: ($jobs | group_by(.size) | map({size: .[0].size} + counts))}
 ' >"$artifacts/progress.json" || return
	jq -r '"Benchmark progress: elapsed=\(.elapsed_seconds)s submitted=\(.submitted) waiting=\(.waiting) active=\(.active) completed=\(.completed)",
  "  GPU occupancy: \(.occupied_gpus)/\(.total_gpus) (\(.gpu_occupancy_percent)%); running requests=\(.running_gpus); nodes busy=\(.busy_nodes)/\(.total_nodes)",
  (.jobs_by_size[] | "  \(.size)-pod jobs: waiting=\(.waiting) active=\(.active) completed=\(.completed)")
 ' "$artifacts/progress.json" | tee -a "$artifacts/progress.log"
}
report_progress_best_effort() {
	local attempt
	for attempt in 1 2 3; do
		if report_progress; then
			return 0
		fi
		echo "warning: progress report failed (attempt $attempt/3)" >&2
		if ((attempt < 3)); then
			sleep 2
		fi
	done
	echo 'warning: skipping progress report after three failed attempts' >&2
}
wait_idle() {
	local deadline=$((SECONDS + ${1:-1200}))
	while ((SECONDS < deadline)); do
		if slurm squeue --noheader --format=%i >"$artifacts/queue.txt" &&
			slurm scontrol show nodes --json=v0.0.44 >"$artifacts/slurm-nodes.json" &&
			[[ ! -s "$artifacts/queue.txt" ]] && jq -e --slurpfile fleet "$artifacts/fleet-names.json" '
			[.nodes[] | select((.state - ["DYNAMIC_NORM"] | sort) == ["EXTERNAL", "IDLE"])
			 | select((.active_features // []) | index("slurm_bridge_gres_compatible")) | .name] as $idle
			 | ($fleet[0] - $idle | length) == 0' "$artifacts/slurm-nodes.json" >/dev/null; then
			return 0
		fi
		sleep 5
	done
	echo 'Timed out waiting for an empty Slurm queue and compatible idle fleet nodes' >&2
	return 1
}
diagnostics() {
	kube get pods -n "$namespace" -o json >"$artifacts/pods-final.json" || true
	kube get events -n "$namespace" -o json >"$artifacts/events-final.json" || true
	slurm scontrol show jobs --json=v0.0.44 >"$artifacts/slurm-jobs.json" || true
	kube get pods -n slurm -o json >"$artifacts/components-after.json" || true
	for component in scheduler controllers admission; do
		kube logs -n slurm "deployment/slurm-bridge-${component}" --all-containers --tail=1000 >"$artifacts/${component}.log" 2>&1 || true
	done
}
finish() {
	local status=$?
	trap - EXIT INT TERM
	[[ -z $knavigator_pid ]] || {
		kill "$knavigator_pid" 2>/dev/null || true
		wait "$knavigator_pid" 2>/dev/null || true
	}
	if $locked; then
		diagnostics
		if $namespace_owned; then
			kube delete namespace "$namespace" --ignore-not-found --wait=true --timeout=300s || status=1
			wait_idle "${BENCHMARK_CLEANUP_TIMEOUT_SECONDS:-300}" || {
				status=1
				slurm scancel --partition=slurm-bridge --full || true
			}
		fi
		kube delete namespace slurm-bridge-benchmark-lock --wait=false || status=1
	fi
	if [[ -f "$artifacts/results.json" ]]; then
		jq -r --argjson status "$status" --slurpfile fleet "$artifacts/fleet.json" '
			def decimal: . * 10 | round / 10;
			(.speedup // 1) as $speedup |
			"Benchmark result: \(if $status == 0 then "PASS" else "FAIL" end)",
			"Fleet: \($fleet[0].items | length) nodes / \(.gpu_capacity) GPU slots; speedup: \($speedup)x",
			"Jobs completed: \([.jobs_by_gang_size[].complete] | add)/\(.jobs)",
			"Pods: \(.pods); failed: \(.pods_failed); unscheduled: \(.pods_unscheduled)",
			"Workload duration: \(.makespan_sec / $speedup / 60 | decimal) min wall / \(.makespan_sec / 3600 | decimal) h simulated",
			"Simulated GPU utilization: \(.gpu_utilization * 100 | decimal)%",
			"Job wait (wall): mean \(.job_wait_sec.mean / $speedup | decimal)s; p95 \(.job_wait_sec.p95 / $speedup | decimal)s; max \(.job_wait_sec.max / $speedup | decimal)s"
		' "$artifacts/results.json" | tee "$artifacts/summary.txt" || status=1
	fi
	jq -n --argjson status "$status" '{exit_code: $status, passed: ($status == 0)}' >"$artifacts/run-result.json"
	echo "Benchmark exit status: $status; artifacts: $artifacts"
	exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

kube get deployment kwok-controller -n kube-system >/dev/null
kube get --raw /apis/scheduling.k8s.io/v1beta1 | jq -e '.resources[] | select(.name == "podgroups")' >/dev/null
# The shared virtual-nodes Helm release requires exclusive ownership of a
# dedicated benchmark cluster. A second invocation fails without deleting work.
kube create namespace slurm-bridge-benchmark-lock
locked=true
kube annotate namespace slurm-bridge-benchmark-lock "benchmark.slinky.slurm.net/run=$run_id"
kube get nodes -l benchmark.slinky.slurm.net/fleet=true -o json | jq '[.items[].metadata.name] | sort' >"$artifacts/fleet-names.json"
wait_idle

# Replace the generic fast-completion stage with the workflow's timed stage.
kube delete stages.kwok.x-k8s.io pod-complete --ignore-not-found
kube apply -f "$artifacts/kwok-stage.yaml"
jq -n --arg run "$run_id" --arg context "$CONTEXT" --arg speedup "$SPEEDUP" \
	--arg knavigator "$KNAVIGATOR_VERSION" --arg bridge "$(git rev-parse HEAD)" \
	'{run: $run, context: $context, speedup: $speedup, knavigator: $knavigator, bridge: $bridge}' >"$artifacts/run.json"

if [[ -z ${KNAVIGATOR_VIRTUAL_NODES_CHART:-} ]]; then
	module_dir="$(go mod download -json "github.com/NVIDIA/knavigator@${KNAVIGATOR_VERSION}" | jq -er .Dir)"
	export KNAVIGATOR_VIRTUAL_NODES_CHART="${module_dir}/charts/virtual-nodes"
fi
cp "$artifacts/fleet-names.json" "$artifacts/previous-fleet.json"
run_workflow "$artifacts/nodes.yaml" 2>&1 | tee "$artifacts/nodes.log"
kube get nodes -l benchmark.slinky.slurm.net/fleet=true -o json >"$artifacts/fleet.json"
jq '[.items[].metadata.name] | sort' "$artifacts/fleet.json" >"$artifacts/fleet-names.json"
# Remove only obsolete nodes owned by the previous benchmark fleet from Slurm.
jq -r --slurpfile current "$artifacts/fleet-names.json" '. - $current[0] | .[]' "$artifacts/previous-fleet.json" |
	while read -r node; do slurm scontrol delete "nodename=$node"; done
# The same idle check includes registration and GRES compatibility, not just names.
wait_idle

# Namespace ownership is established atomically; never delete an existing run.
kube create namespace "$namespace"
namespace_owned=true
started=$SECONDS
run_workflow "$artifacts/workload.yaml" -speedup "$SPEEDUP" -results "$artifacts" -results.timeout "$TIMEOUT" > >(tee "$artifacts/knavigator.log") 2>&1 &
knavigator_pid=$!
next_report=$SECONDS
while kill -0 "$knavigator_pid" 2>/dev/null; do
	if ((SECONDS >= next_report)); then
		next_report=$((SECONDS + 60))
		report_progress_best_effort
	fi
	sleep 1
done
wait "$knavigator_pid"
knavigator_pid=""
report_progress_best_effort
jq -e '([.jobs_by_gang_size[].complete] | add) == .jobs' "$artifacts/results.json"
# Pod observations can finish before the Job controller records its outcome.
deadline=$((SECONDS + 60))
while true; do
	kube get jobs -n "$namespace" -l app.kubernetes.io/managed-by=knavigator -o json >"$artifacts/jobs-live.json"
	if jq -e 'any(.items[].status.conditions[]?; (.type == "Failed" or .type == "FailureTarget") and .status == "True")' "$artifacts/jobs-live.json" >/dev/null; then
		jq -r '.items[] | select(any(.status.conditions[]?; (.type == "Failed" or .type == "FailureTarget") and .status == "True")) | "FAIL: Job \(.metadata.name) failed"' "$artifacts/jobs-live.json" >&2
		exit 1
	fi
	if jq -e --slurpfile results "$artifacts/results.json" '
  (.items | length) == $results[0].jobs and
  all(.items[]; any(.status.conditions[]?; .type == "Complete" and .status == "True"))
 ' "$artifacts/jobs-live.json" >/dev/null; then
		break
	fi
	if ((SECONDS >= deadline)); then
		echo 'FAIL: timed out waiting for successful Job conditions' >&2
		exit 1
	fi
	sleep 2
done
echo 'PASS: all benchmark workloads completed'
# EXIT captures diagnostics and verifies cleanup releases every Slurm allocation.
