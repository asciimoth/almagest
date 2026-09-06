#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${E2E_IMAGE:-almagest-e2e:local}"
coverage_enabled="${E2E_COVERAGE:-0}"
coverage_dir="${E2E_COVERAGE_DIR:-$repo_root/e2e/coverage}"
coverpkg="${E2E_COVERPKG:-github.com/asciimoth/almagest/bin/...,github.com/asciimoth/almagest/lib/...,github.com/asciimoth/almagest/certcat/...}"

cd "$repo_root"

build_args=()
coverage_inputs=()
if [[ "$coverage_enabled" == "1" ]]; then
	mkdir -p "$coverage_dir"
	rm -rf "$coverage_dir/raw"
	rm -f "$coverage_dir/coverage.out" "$coverage_dir/functions.txt" "$coverage_dir/summary.txt" "$coverage_dir/total.txt"
	mkdir -p "$coverage_dir/raw"
	build_args=(
		--build-arg E2E_COVERAGE=1
		--build-arg "E2E_COVERPKG=$coverpkg"
	)
	echo "==> e2e coverage output: $coverage_dir"
fi

if (($# > 0)); then
	cases=("$@")
else
	case_find_args=(e2e/cases -mindepth 1 -maxdepth 1 -type d)
	if [[ "${E2E_INCLUDE_PRIVILEGED:-}" != "1" && "${E2E_DOCKER_RUN_ARGS:-}" != *"--privileged"* ]]; then
		case_find_args+=( '!' -exec test -e '{}/requires-privileged' ';' )
	fi
	mapfile -t cases < <(find "${case_find_args[@]}" -printf '%f\n' | sort)
fi

if ((${#cases[@]} == 0)); then
	echo "no e2e cases found" >&2
	exit 1
fi

declare -A built_images=()

for case_name in "${cases[@]}"; do
	echo "==> e2e: $case_name"
	case_image="$image"
	case_build_args=("${build_args[@]}")
	if [[ -f "e2e/cases/$case_name/docker-build-args" ]]; then
		case_image="$image-$case_name"
		while IFS= read -r build_arg; do
			[[ -z "$build_arg" || "$build_arg" == \#* ]] && continue
			case_build_args+=(--build-arg "$build_arg")
		done <"e2e/cases/$case_name/docker-build-args"
	fi
	if [[ -z "${built_images[$case_image]:-}" ]]; then
		docker build "${case_build_args[@]}" -f e2e/Dockerfile -t "$case_image" .
		built_images[$case_image]=1
	fi
	docker_run_args=()
	if [[ "$coverage_enabled" == "1" ]]; then
		case_coverage_dir="$coverage_dir/raw/$case_name"
		mkdir -p "$case_coverage_dir"
		coverage_inputs+=("$case_coverage_dir")
		docker_run_args=(
			-e GOCOVERDIR=/e2e-coverage
			-v "$case_coverage_dir:/e2e-coverage"
		)
	fi
	# shellcheck disable=SC2086
	docker run --rm ${E2E_DOCKER_RUN_ARGS:-} "${docker_run_args[@]}" "$case_image" "$case_name"
done

if [[ "$coverage_enabled" == "1" ]]; then
	input_dirs="$(IFS=,; printf '%s' "${coverage_inputs[*]}")"
	go tool covdata percent -i="$input_dirs" | tee "$coverage_dir/summary.txt"
	go tool covdata textfmt -i="$input_dirs" -o "$coverage_dir/coverage.out"
	go tool cover -func="$coverage_dir/coverage.out" >"$coverage_dir/functions.txt"
	tail -1 "$coverage_dir/functions.txt" | tee "$coverage_dir/total.txt"
fi
