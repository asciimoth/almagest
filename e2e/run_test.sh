#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

docker() {
	local case_name

	case "${1:-}" in
		image)
			return 1
			;;
		build)
			return 0
			;;
		run)
			case_name="${!#}"
			if [[ "$case_name" == "admin_api_transports" ]]; then
				sleep 0.01
				echo "passing case output"
				return 0
			fi
			sleep 0.03
			echo "failing case output"
			return 7
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
	E2E_JOBS=2 E2E_RANDOM_SEED=1 \
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
assert_contains "PASS admin_api_transports ("
assert_contains "failing case output"
assert_contains "FAIL amnesia_selfhost_controller (exit 7, "
assert_contains "completed 2 cases in "
assert_contains "slowest 2 cases"

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

echo "e2e run harness tests passed"
