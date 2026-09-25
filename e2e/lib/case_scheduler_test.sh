#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # The path is resolved from this script's directory.
source "$(dirname "${BASH_SOURCE[0]}")/case_scheduler.sh"

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/normal" "$root/exclusive"
touch "$root/exclusive/exclusive"

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

echo "e2e case scheduler tests passed"
