#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${E2E_IMAGE:-almagest-e2e:local}"
coverage_enabled="${E2E_COVERAGE:-0}"
coverage_dir="${E2E_COVERAGE_DIR:-$repo_root/e2e/coverage}"
coverpkg="${E2E_COVERPKG:-github.com/asciimoth/almagest/bin/...,github.com/asciimoth/almagest/lib/...,github.com/asciimoth/almagest/certcat/...}"
build_attempts="${E2E_BUILD_ATTEMPTS:-10}"
build_retry_delay="${E2E_BUILD_RETRY_DELAY:-1}"
build_retry_max_delay="${E2E_BUILD_RETRY_MAX_DELAY:-60}"
image_input_label="io.almagest.e2e.input-sha256"

cd "$repo_root"

for integer_setting in \
	"E2E_BUILD_ATTEMPTS=$build_attempts" \
	"E2E_BUILD_RETRY_DELAY=$build_retry_delay" \
	"E2E_BUILD_RETRY_MAX_DELAY=$build_retry_max_delay"; do
	setting_name="${integer_setting%%=*}"
	value="${integer_setting#*=}"
	if [[ ! "$value" =~ ^[0-9]+$ ]]; then
		echo "$setting_name must be a non-negative integer" >&2
		exit 2
	fi
done
if ((build_attempts == 0)); then
	echo "E2E_BUILD_ATTEMPTS must be greater than zero" >&2
	exit 2
fi

docker_build_with_retry() {
	local attempt=1
	local delay="$build_retry_delay"
	local status=0
	if ((delay > build_retry_max_delay)); then
		delay="$build_retry_max_delay"
	fi

	while true; do
		if docker build "$@"; then
			return 0
		else
			status=$?
		fi
		if ((attempt >= build_attempts)); then
			echo "docker build failed after $attempt attempts" >&2
			return "$status"
		fi
		echo "docker build attempt $attempt/$build_attempts failed; retrying in ${delay}s" >&2
		sleep "$delay"
		((attempt += 1))
		if ((delay < build_retry_max_delay)); then
			delay=$((delay * 2))
			if ((delay > build_retry_max_delay)); then
				delay="$build_retry_max_delay"
			fi
		fi
	done
}

hash_image_inputs() {
	local path
	{
		find .dockerignore go.work go.work.sum bin certcat lib e2e \
			-path e2e/coverage -prune -o \
			\( -type f -o -type l \) -print0 \
			| sort -z \
			| while IFS= read -r -d '' path; do
				printf '%s\0' "$path" "$(stat -c '%f' "$path")"
				if [[ -L "$path" ]]; then
					printf '%s\0' "$(readlink "$path")"
				else
					sha256sum --zero "$path"
				fi
			done
	} | sha256sum | cut -d ' ' -f 1
}

image_matches_inputs() {
	local image_name="$1"
	local input_hash="$2"
	local image_hash

	image_hash="$(docker image inspect \
		--format "{{ index .Config.Labels \"$image_input_label\" }}" \
		"$image_name" 2>/dev/null || true)"
	[[ "$image_hash" == "$input_hash" ]]
}

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
source_input_hash="$(hash_image_inputs)"

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
		case_input_hash="$({
			printf '%s\0' "$source_input_hash"
			printf '%s\0' "${case_build_args[@]}"
		} | sha256sum | cut -d ' ' -f 1)"
		if [[ "${E2E_REBUILD:-0}" != "1" ]] && image_matches_inputs "$case_image" "$case_input_hash"; then
			echo "==> e2e: reusing unchanged image $case_image"
		else
			docker_build_with_retry "${case_build_args[@]}" \
				--label "$image_input_label=$case_input_hash" \
				-f e2e/Dockerfile -t "$case_image" .
		fi
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
