#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
coverage_dir="${GO_COVERAGE_DIR:-$repo_root/coverage/regular}"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/almagest-go-coverage.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT

combined_profile="$work_dir/coverage.out"
functions_report="$work_dir/functions.txt"
summary_report="$work_dir/summary.txt"
total_report="$work_dir/total.txt"
modules_dir="$work_dir/modules"

if (($# > 0)); then
	modules=("$@")
else
	modules=(certcat lib bin)
fi

mkdir -p "$modules_dir"

echo "==> regular go coverage output: $coverage_dir"

profiles=()
for module in "${modules[@]}"; do
	profile="$modules_dir/$module.out"
	profiles+=("$profile")
	case "$module" in
		certcat)
			go -C certcat test ./... -covermode=atomic -coverpkg=./... -coverprofile="$profile"
			;;
		lib)
			go -C lib test ./... --race -covermode=atomic -coverpkg=./... -coverprofile="$profile"
			;;
		bin)
			go -C bin test ./... --race -covermode=atomic -coverpkg=./... -coverprofile="$profile"
			;;
		*)
			echo "unknown go coverage module: $module" >&2
			exit 1
			;;
	esac
done

printf 'mode: atomic\n' >"$combined_profile"
for profile in "${profiles[@]}"; do
	tail -n +2 "$profile" | awk 'NF > 0' >>"$combined_profile"
done

"$repo_root/scripts/go-cover-functions.sh" "$combined_profile" "$functions_report"
tail -1 "$functions_report" | tee "$summary_report" "$total_report"

publish_file() {
	local source="$1"
	local target="$2"
	local target_tmp="$target.tmp.$$"
	mkdir -p "$(dirname "$target")"
	cp "$source" "$target_tmp"
	mv "$target_tmp" "$target"
}

publish_file "$combined_profile" "$coverage_dir/coverage.out"
publish_file "$functions_report" "$coverage_dir/functions.txt"
publish_file "$summary_report" "$coverage_dir/summary.txt"
publish_file "$total_report" "$coverage_dir/total.txt"
for module in "${modules[@]}"; do
	publish_file "$modules_dir/$module.out" "$coverage_dir/modules/$module.out"
done
