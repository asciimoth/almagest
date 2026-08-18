#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
regular_dir="${GO_COVERAGE_DIR:-$repo_root/coverage/regular}"
e2e_dir="${E2E_COVERAGE_DIR:-$repo_root/e2e/coverage}"

display_path() {
	local path="$1"
	case "$path" in
		"$repo_root"/*)
			printf '%s\n' "${path#"$repo_root/"}"
			;;
		*)
			printf '%s\n' "$path"
			;;
	esac
}

percent_from_total() {
	local total_file="$1"
	if [[ ! -f "$total_file" ]]; then
		printf 'unavailable'
		return
	fi
	awk '/^total:/ { percent = $NF } END { if (percent == "") print "unavailable"; else print percent }' "$total_file"
}

regular_percent="$(percent_from_total "$regular_dir/total.txt")"
e2e_percent="$(percent_from_total "$e2e_dir/total.txt")"

echo "==> final coverage report"
echo "regular go tests: $regular_percent statements"
echo "docker e2e tests: $e2e_percent statements"
echo "regular go profile: $(display_path "$regular_dir/coverage.out")"
echo "docker e2e profile: $(display_path "$e2e_dir/coverage.out")"
