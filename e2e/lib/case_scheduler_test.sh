#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # The path is resolved from this script's directory.
source "$(dirname "${BASH_SOURCE[0]}")/case_scheduler.sh"

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/normal" "$root/exclusive" "$root/privileged"
touch "$root/exclusive/exclusive"
touch "$root/privileged/requires-privileged"

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

normal_args=()
e2e_add_case_privileges "$root/normal" normal_args
if ((${#normal_args[@]} != 0)); then
	echo "normal case received privileged Docker arguments" >&2
	exit 1
fi

privileged_args=()
e2e_add_case_privileges "$root/privileged" privileged_args
if [[ "${privileged_args[*]}" != "--privileged --device /dev/net/tun" ]]; then
	echo "privileged case did not receive the required Docker arguments" >&2
	exit 1
fi
if [[ "$(e2e_case_privilege "$root/normal")" != "regular" ]]; then
	echo "normal case did not report regular status" >&2
	exit 1
fi
if [[ "$(e2e_case_privilege "$root/privileged")" != "privileged" ]]; then
	echo "privileged case did not report privileged status" >&2
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
