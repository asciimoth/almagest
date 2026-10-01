#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
regular_dir="${GO_COVERAGE_DIR:-$repo_root/coverage/regular}"
e2e_dir="${E2E_COVERAGE_DIR:-$repo_root/e2e/coverage}"
winvm_dir="${WINVM_COVERAGE_DIR:-$repo_root/coverage/winvm}"
total_dir="${TOTAL_COVERAGE_DIR:-$repo_root/coverage/total}"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/almagest-total-coverage.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT

combined_profile="$work_dir/coverage.out"
functions_report="$work_dir/functions.txt"
summary_report="$work_dir/summary.txt"
total_report="$work_dir/total.txt"

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

publish_file() {
	local source="$1"
	local target="$2"
	local target_tmp="$target.tmp.$$"
	mkdir -p "$(dirname "$target")"
	cp "$source" "$target_tmp"
	mv "$target_tmp" "$target"
}

combine_profiles() {
	local output="$1"
	shift

	awk '
		FNR == 1 {
			if ($1 != "mode:" || NF != 2) {
				printf "invalid coverage mode line in %s\n", FILENAME > "/dev/stderr"
				exit 1
			}
			if (mode == "") {
				mode = $2
			} else if (mode != $2) {
				printf "coverage mode mismatch in %s: got %s, want %s\n", FILENAME, $2, mode > "/dev/stderr"
				exit 1
			}
			next
		}
		NF == 0 { next }
		NF != 3 {
			printf "invalid coverage record in %s at line %d\n", FILENAME, FNR > "/dev/stderr"
			exit 1
		}
		{
			location = $1
			if (location in statements && statements[location] != $2) {
				printf "statement count mismatch for %s\n", location > "/dev/stderr"
				exit 1
			}
			statements[location] = $2
			counts[location] += $3
		}
		END {
			if (mode == "") {
				exit 1
			}
			printf "mode: %s\n", mode
			for (location in statements) {
				printf "%s %s %.0f\n", location, statements[location], counts[location]
			}
		}
	' "$@" >"$output"
}

profiles=("$regular_dir/coverage.out" "$e2e_dir/coverage.out")
if [[ -f "$winvm_dir/coverage.out" ]]; then
	profiles+=("$winvm_dir/coverage.out")
fi
combine_profiles "$combined_profile" "${profiles[@]}"
go tool cover -func="$combined_profile" >"$functions_report"
tail -1 "$functions_report" | tee "$summary_report" "$total_report"

publish_file "$combined_profile" "$total_dir/coverage.out"
publish_file "$functions_report" "$total_dir/functions.txt"
publish_file "$summary_report" "$total_dir/summary.txt"
publish_file "$total_report" "$total_dir/total.txt"

regular_percent="$(percent_from_total "$regular_dir/total.txt")"
e2e_percent="$(percent_from_total "$e2e_dir/total.txt")"
winvm_percent="$(percent_from_total "$winvm_dir/total.txt")"
total_percent="$(percent_from_total "$total_dir/total.txt")"

echo "==> final coverage report"
echo "regular go tests: $regular_percent statements"
echo "docker e2e tests: $e2e_percent statements"
if [[ -f "$winvm_dir/coverage.out" ]]; then
	echo "Windows VM tests: $winvm_percent statements"
fi
echo "total: $total_percent statements"
echo "regular go profile: $(display_path "$regular_dir/coverage.out")"
echo "docker e2e profile: $(display_path "$e2e_dir/coverage.out")"
if [[ -f "$winvm_dir/coverage.out" ]]; then
	echo "Windows VM profile: $(display_path "$winvm_dir/coverage.out")"
fi
echo "total profile: $(display_path "$total_dir/coverage.out")"
