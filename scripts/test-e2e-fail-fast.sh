#!/usr/bin/env bash
set -euo pipefail

docker_recipe="${1:-test-e2e-docker}"
windows_recipe="${2:-test-winvm-enabled}"
cleanup_timeout="${E2E_SIBLING_CLEANUP_TIMEOUT:-180}"
declare -A running=()

if [[ ! "$cleanup_timeout" =~ ^[0-9]+$ ]] || ((10#$cleanup_timeout == 0)); then
	echo "E2E_SIBLING_CLEANUP_TIMEOUT must be a positive integer" >&2
	exit 2
fi

start_recipe() {
	local recipe="$1"
	setsid just "$recipe" &
	running[$!]="$recipe"
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

trap 'interrupt_running; exit 130' INT TERM

start_recipe "$docker_recipe"
start_recipe "$windows_recipe"

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
