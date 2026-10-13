#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
cleanup_timeout="${E2E_SIBLING_CLEANUP_TIMEOUT:-180}"
parallel_types="${TEST_TYPES_PARALLEL:-0}"
declare -A running=()
resource_root=${TEST_RESOURCE_ROOT:-$repo_root/artifacts/test-resources}
mkdir -p -- "$resource_root"
resource_dir=$(mktemp -d "$resource_root/run-XXXXXXXX")
python3 "$repo_root/dev/winvm/tools/host-resources.py" \
	--output "$resource_dir/host-resources.jsonl" --interval 2 \
	--exit-with-pid "$$" --pid "test-scheduler=$$" \
	>"$resource_dir/resource-monitor.log" 2>&1 &
resource_monitor_pid=$!
echo "==> test types: resource evidence: $resource_dir"

if (($#)); then
	recipes=("$@")
else
	recipes=(test-e2e-docker test-winvm-enabled)
fi

if [[ ! "$cleanup_timeout" =~ ^[0-9]+$ ]] || ((10#$cleanup_timeout == 0)); then
	echo "E2E_SIBLING_CLEANUP_TIMEOUT must be a positive integer" >&2
	exit 2
fi
if [[ "$parallel_types" != "0" && "$parallel_types" != "1" ]]; then
	echo "TEST_TYPES_PARALLEL must be 0 or 1" >&2
	exit 2
fi

start_recipe() {
	local recipe="$1"
	setsid just "$recipe" &
	started_pid=$!
	running[$started_pid]="$recipe"
}

interrupt_running() {
	local pid
	local deadline

	trap - INT TERM
	for pid in "${!running[@]}"; do
		kill -TERM -- "-$pid" 2>/dev/null || true
	done
	for pid in "${!running[@]}"; do
		deadline=$((SECONDS + cleanup_timeout))
		while kill -0 -- "-$pid" 2>/dev/null; do
			if ((SECONDS >= deadline)); then
				echo "==> test-e2e: cleanup timed out for ${running[$pid]}; killing its process group" >&2
				kill -KILL -- "-$pid" 2>/dev/null || true
				break
			fi
			sleep 0.1
		done
		wait "$pid" 2>/dev/null || true
	done
}

finish() {
	local status=$?
	kill -TERM "$resource_monitor_pid" 2>/dev/null || true
	wait "$resource_monitor_pid" 2>/dev/null || true
	if ((status == 0)); then
		rm -rf -- "$resource_dir"
	else
		echo "==> test types: retained resource evidence: $resource_dir" >&2
	fi
}

trap finish EXIT
trap 'interrupt_running; exit 130' INT TERM

if [[ "$parallel_types" == "0" ]]; then
	for recipe in "${recipes[@]}"; do
		echo "==> test types: starting $recipe [serial]"
		start_recipe "$recipe"
		status=0
		wait "$started_pid" || status=$?
		unset 'running[$started_pid]'
		if ((status != 0)); then
			echo "==> test types: FAIL $recipe (exit $status)" >&2
			exit "$status"
		fi
		echo "==> test types: PASS $recipe"
	done
	exit 0
fi

for recipe in "${recipes[@]}"; do
	start_recipe "$recipe"
done

while ((${#running[@]} > 0)); do
	completed_pid=""
	status=0
	if wait -n -p completed_pid "${!running[@]}"; then
		status=0
	else
		status=$?
	fi
	completed_recipe="${running[$completed_pid]}"
	unset 'running[$completed_pid]'
	if ((status != 0)); then
		echo "==> test-e2e: $completed_recipe failed; interrupting concurrent tests" >&2
		interrupt_running
		exit "$status"
	fi
done
