#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_state="$(mktemp -d "${TMPDIR:-/tmp}/almagest-e2e-run-test.XXXXXX")"
trap 'rm -rf "$test_state"' EXIT

docker() {
	local case_name
	local cidfile=""
	local previous=""
	if [[ -n "${E2E_TEST_COMMANDS:-}" ]]; then
		printf '%s\n' "$1" >>"$E2E_TEST_COMMANDS"
	fi

	case "${1:-}" in
		ps)
			if [[ -n "${E2E_RESOURCE_TEST_STATE:-}" ]]; then
				[[ "$*" == *"label=io.almagest.e2e.run=almagest-e2e-run."* ]] || return 2
				echo orphan-helper
			fi
			return 0
			;;
		network|volume)
			if [[ -n "${E2E_RESOURCE_TEST_STATE:-}" ]]; then
				if [[ "$2" == ls ]]; then
					[[ "$*" == *"label=io.almagest.e2e.run=almagest-e2e-run."* ]] || return 2
					echo "orphan-$1"
				elif [[ "$2" == rm && "$3" == "orphan-$1" ]]; then
					touch "$E2E_RESOURCE_TEST_STATE/$1-removed"
				fi
			fi
			return 0
			;;
		logs)
			echo "retained helper output"
			return 0
			;;
		image)
			return 1
			;;
		build)
			return 0
			;;
		run)
			case_name="${!#}"
			if [[ -n "${E2E_INTERRUPT_TEST_STATE:-}" ]]; then
				for argument in "$@"; do
					if [[ "$previous" == "--cidfile" ]]; then
						cidfile="$argument"
						break
					fi
					previous="$argument"
				done
				printf 'interrupt-test-container\n' >"$cidfile"
				touch "$E2E_INTERRUPT_TEST_STATE/started"
				while [[ ! -e "$E2E_INTERRUPT_TEST_STATE/removed" ]]; do
					sleep 0.01
				done
				return 0
			fi
			if [[ "$case_name" == "admin_api_transports" ]]; then
				sleep 0.03
				echo "passing case output"
				return 0
			fi
			if [[ "$case_name" == "config_editing_api" || "$case_name" == "tun_high_load" ]]; then
				sleep 0.01
				echo "passing case output"
				return 0
			fi
			sleep 0.03
			echo "failing case output"
			return 7
			;;
		rm)
			if [[ -n "${E2E_RESOURCE_TEST_STATE:-}" && "${!#}" == orphan-helper ]]; then
				touch "$E2E_RESOURCE_TEST_STATE/helper-removed"
				return 0
			fi
			if [[ -n "${E2E_INTERRUPT_TEST_STATE:-}" ]]; then
				touch "$E2E_INTERRUPT_TEST_STATE/removed"
				return 0
			fi
			return 2
			;;
		*)
			echo "unexpected docker command: $*" >&2
			return 2
			;;
	esac
}
export -f docker

output=""
if output="$(
	cd "$repo_root"
	E2E_FAILURE_LOG_ROOT="$test_state/failure-logs" \
		E2E_FAIL_FAST=0 E2E_JOBS=2 E2E_RANDOM_SEED=1 \
		./e2e/run.sh admin_api_transports amnesia_selfhost_controller 2>&1
)"; then
	echo "mixed passing and failing harness run succeeded" >&2
	exit 1
fi

assert_contains() {
	local expected="${1:?expected output is required}"

	if [[ "$output" != *"$expected"* ]]; then
		printf 'missing harness output: %s\n%s\n' "$expected" "$output" >&2
		exit 1
	fi
}

assert_contains "starting admin_api_transports [regular]"
assert_contains "starting amnesia_selfhost_controller [privileged]"
assert_contains "2 cases, 2 jobs, 0 exclusive"
assert_contains "PASS admin_api_transports ("
assert_contains "failing case output"
assert_contains "FAIL amnesia_selfhost_controller (exit 7, "
assert_contains "completed 2 cases in "
assert_contains "slowest 2 cases"
failure_log="$(find "$test_state/failure-logs" -type f \
	-name amnesia_selfhost_controller.log -print -quit)"
if [[ -z "$failure_log" ]] || ! grep -q 'failing case output' "$failure_log"; then
	echo "harness did not retain the failed case log" >&2
	exit 1
fi

if [[ "$output" == *"passing case output"* ]]; then
	echo "harness showed output from a passing case" >&2
	exit 1
fi
if [[ ! "$output" =~ PASS\ admin_api_transports\ \([0-9]+\.[0-9]{3}s\) ]]; then
	echo "passing case did not include a millisecond duration" >&2
	exit 1
fi
if [[ ! "$output" =~ FAIL\ amnesia_selfhost_controller\ \(exit\ 7,\ [0-9]+\.[0-9]{3}s\) ]]; then
	echo "failing case did not include a millisecond duration" >&2
	exit 1
fi

fail_fast_output=""
if fail_fast_output="$(
	cd "$repo_root"
	E2E_FAILURE_LOG_ROOT="$test_state/failure-logs" E2E_JOBS=1 E2E_RANDOM_SEED=0 \
		./e2e/run.sh amnesia_selfhost_controller admin_api_transports 2>&1
)"; then
	echo "fail-fast harness run succeeded" >&2
	exit 1
fi
if [[ "$fail_fast_output" != *"FAIL amnesia_selfhost_controller (exit 7, "* ]]; then
	echo "fail-fast harness did not report the failed case" >&2
	printf '%s\n' "$fail_fast_output" >&2
	exit 1
fi
if [[ "$fail_fast_output" == *"starting admin_api_transports"* ]]; then
	echo "fail-fast harness started a case after the first failure" >&2
	printf '%s\n' "$fail_fast_output" >&2
	exit 1
fi
if [[ "$fail_fast_output" != *"e2e failed fast: amnesia_selfhost_controller"* ]]; then
	echo "fail-fast harness did not report its stopping case" >&2
	printf '%s\n' "$fail_fast_output" >&2
	exit 1
fi

scheduling_output="$(
	cd "$repo_root"
	E2E_JOBS=2 E2E_RANDOM_SEED=5 \
		./e2e/run.sh admin_api_transports tun_high_load config_editing_api 2>&1
)"

line_number() {
	local expected="${1:?expected output is required}"
	local input="${2:?input is required}"

	grep -nF -m1 "$expected" <<<"$input" | cut -d: -f1
}

admin_start="$(line_number "starting admin_api_transports" "$scheduling_output")"
config_start="$(line_number "starting config_editing_api" "$scheduling_output")"
admin_pass="$(line_number "PASS admin_api_transports" "$scheduling_output")"
config_pass="$(line_number "PASS config_editing_api" "$scheduling_output")"
exclusive_start="$(line_number "starting tun_high_load" "$scheduling_output")"

if ((config_start <= admin_start || config_start >= admin_pass || config_start >= config_pass)); then
	echo "exclusive case prevented a later regular case from starting" >&2
	printf '%s\n' "$scheduling_output" >&2
	exit 1
fi
if ((exclusive_start <= admin_pass || exclusive_start <= config_pass)); then
	echo "exclusive case started before the regular cases finished" >&2
	printf '%s\n' "$scheduling_output" >&2
	exit 1
fi

interrupt_state="$test_state/interrupt"
mkdir -p "$interrupt_state"
(
	cd "$repo_root"
	E2E_INTERRUPT_TEST_STATE="$interrupt_state" E2E_JOBS=1 E2E_RANDOM_SEED=1 \
		./e2e/run.sh admin_api_transports
) >"$interrupt_state/output" 2>&1 &
harness_pid=$!
for _ in {1..500}; do
	[[ -e "$interrupt_state/started" ]] && break
	sleep 0.01
done
if [[ ! -e "$interrupt_state/started" ]]; then
	echo "interrupted harness did not start its container" >&2
	exit 1
fi
kill -TERM "$harness_pid"
interrupt_status=0
wait "$harness_pid" || interrupt_status=$?
if ((interrupt_status != 130)); then
	printf 'interrupted harness exited with %d\n' "$interrupt_status" >&2
	cat "$interrupt_state/output" >&2
	exit 1
fi
if [[ ! -e "$interrupt_state/removed" ]]; then
	echo "interrupted harness did not remove its container" >&2
	exit 1
fi

mkdir -p "$interrupt_state/coverage/raw"
touch "$interrupt_state/coverage/raw/keep"
E2E_COVERAGE=1 E2E_COVERAGE_DIR="$interrupt_state/coverage" \
	E2E_TEST_COMMANDS="$interrupt_state/prepare-commands" \
	./e2e/run.sh --prepare-only admin_api_transports >"$interrupt_state/prepare-output"
if [[ ! -f "$interrupt_state/coverage/raw/keep" ]]; then
	echo "preparation removed existing coverage data" >&2
	exit 1
fi
if ! grep -qx build "$interrupt_state/prepare-commands" || grep -qx run "$interrupt_state/prepare-commands"; then
	echo "prepare-only must build without running tests" >&2
	exit 1
fi
E2E_TEST_COMMANDS="$interrupt_state/loop-commands" E2E_LOG_DIR="$interrupt_state/logs" \
	./e2e/run.sh --skip-build admin_api_transports >"$interrupt_state/loop-output"
if grep -Eq '^(build|image)$' "$interrupt_state/loop-commands"; then
	echo "prepared loop must not build or inspect images" >&2
	exit 1
fi
if ! grep -q 'passing case output' "$interrupt_state/logs/admin_api_transports.log"; then
	echo "case output was not retained" >&2
	exit 1
fi

E2E_RESOURCE_TEST_STATE="$interrupt_state" E2E_LOG_DIR="$interrupt_state/resource-logs" \
	./e2e/run.sh --skip-build admin_api_transports >"$interrupt_state/resource-output"
for resource in helper network volume; do
	if [[ ! -f "$interrupt_state/$resource-removed" ]]; then
		echo "scheduler did not remove orphan $resource" >&2
		exit 1
	fi
done
if ! grep -q 'retained helper output' "$interrupt_state/resource-logs/helper-orphan-helper.log"; then
	echo "orphan helper output was not retained" >&2
	exit 1
fi

echo "e2e run harness tests passed"
