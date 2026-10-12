#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # The path is resolved from this script's directory.
source "$(dirname "${BASH_SOURCE[0]}")/case_scheduler.sh"

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/normal" "$root/normal-later" "$root/exclusive" "$root/privileged" "$root/direct"
touch "$root/exclusive/exclusive"
touch "$root/privileged/requires-privileged"
touch "$root/direct/requires-direct"

e2e_case_can_start "$root/normal" 0 0
e2e_case_can_start "$root/normal" 3 0
e2e_case_can_start "$root/exclusive" 0 0

if e2e_case_can_start "$root/exclusive" 1 0; then
	echo "exclusive case started beside a running case" >&2
	exit 1
fi
if e2e_case_can_start "$root/normal" 0 1; then
	echo "normal case started while an exclusive case was running" >&2
	exit 1
fi
if e2e_case_can_start "$root/exclusive" 0 1; then
	echo "second exclusive case started while an exclusive case was running" >&2
	exit 1
fi

# shellcheck disable=SC2034 # The scheduler reads both arrays through namerefs.
scheduled_cases=(normal exclusive normal-later)
# shellcheck disable=SC2034 # The scheduler reads both arrays through namerefs.
pending_cases=(0 1 2)
unset 'pending_cases[0]'
startable_index="$(e2e_find_startable_case \
	"$root" scheduled_cases pending_cases 1 0)"
if [[ "$startable_index" != "2" ]]; then
	echo "exclusive case blocked a later normal case" >&2
	exit 1
fi
startable_index="$(e2e_find_startable_case \
	"$root" scheduled_cases pending_cases 0 0)"
if [[ "$startable_index" != "1" ]]; then
	echo "scheduler did not retain shuffled order when all cases could start" >&2
	exit 1
fi
if e2e_find_startable_case "$root" scheduled_cases pending_cases 1 1 >/dev/null; then
	echo "scheduler selected a case while an exclusive case was running" >&2
	exit 1
fi

normal_args=()
e2e_add_case_privileges "$root/normal" normal_args
if [[ "${normal_args[*]}" != "--privileged --device /dev/net/tun" ]]; then
	echo "isolated case did not receive privileged Docker arguments" >&2
	exit 1
fi

direct_args=()
e2e_add_case_privileges "$root/direct" direct_args
if ((${#direct_args[@]} != 0)); then
	echo "direct case received privileged Docker arguments" >&2
	exit 1
fi

privileged_args=()
e2e_add_case_privileges "$root/privileged" privileged_args
if [[ "${privileged_args[*]}" != "--privileged --device /dev/net/tun" ]]; then
	echo "privileged case did not receive the required Docker arguments" >&2
	exit 1
fi
if [[ "$(e2e_case_privilege "$root/normal")" != "isolated-privileged" ]]; then
	echo "isolated case did not report isolated privileged status" >&2
	exit 1
fi
if [[ "$(e2e_case_privilege "$root/privileged")" != "isolated-privileged" ]]; then
	echo "privileged isolation case did not report isolated privileged status" >&2
	exit 1
fi
if [[ "$(e2e_case_privilege "$root/direct")" != "direct" ]]; then
	echo "direct case did not report direct status" >&2
	exit 1
fi

if [[ "$(e2e_format_duration 1234)" != "1.234s" ]]; then
	echo "sub-minute duration was formatted incorrectly" >&2
	exit 1
fi
if [[ "$(e2e_format_duration 62345)" != "1m 02.345s" ]]; then
	echo "minute duration was formatted incorrectly" >&2
	exit 1
fi
if [[ "$(e2e_format_duration 3723456)" != "1h 02m 03.456s" ]]; then
	echo "hour duration was formatted incorrectly" >&2
	exit 1
fi

start_ms="$(e2e_now_ms)"
end_ms="$(e2e_now_ms)"
if ((end_ms < start_ms)); then
	echo "millisecond clock moved backwards" >&2
	exit 1
fi

original=(alpha bravo charlie delta echo foxtrot golf hotel)
first=("${original[@]}")
repeated=("${original[@]}")
second=("${original[@]}")
e2e_shuffle_cases 12345 first
e2e_shuffle_cases 12345 repeated
e2e_shuffle_cases 54321 second

if [[ "${first[*]}" != "${repeated[*]}" ]]; then
	echo "same random seed produced different case orders" >&2
	exit 1
fi
if [[ "${first[*]}" == "${second[*]}" ]]; then
	echo "different random seeds produced the same case order" >&2
	exit 1
fi
if [[ "$(printf '%s\n' "${first[@]}" | sort)" != "$(printf '%s\n' "${original[@]}" | sort)" ]]; then
	echo "case shuffle changed the set of cases" >&2
	exit 1
fi

echo "e2e case scheduler tests passed"
