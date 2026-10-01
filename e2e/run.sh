#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091 # The path is resolved from the repository root.
source "$repo_root/e2e/lib/case_scheduler.sh"
image="${E2E_IMAGE:-almagest-e2e:local}"
coverage_enabled="${E2E_COVERAGE:-0}"
coverage_dir="${E2E_COVERAGE_DIR:-$repo_root/e2e/coverage}"
coverpkg="${E2E_COVERPKG:-github.com/asciimoth/almagest/bin/...,github.com/asciimoth/almagest/lib/...,github.com/asciimoth/almagest/certcat/...}"
jobs="${E2E_JOBS:-4}"
build_attempts="${E2E_BUILD_ATTEMPTS:-10}"
build_retry_delay="${E2E_BUILD_RETRY_DELAY:-1}"
build_retry_max_delay="${E2E_BUILD_RETRY_MAX_DELAY:-60}"
image_input_label="io.almagest.e2e.input-sha256"
random_seed="${E2E_RANDOM_SEED:-}"
slowest_count=15

if [[ -z "$random_seed" ]]; then
	random_seed="$(od -An -N4 -tu4 /dev/urandom)"
	random_seed="${random_seed//[[:space:]]/}"
	random_seed=$((random_seed & 2147483647))
fi

cd "$repo_root"

for integer_setting in \
	"E2E_JOBS=$jobs" \
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
if ((jobs == 0)); then
	echo "E2E_JOBS must be greater than zero" >&2
	exit 2
fi
if ((build_attempts == 0)); then
	echo "E2E_BUILD_ATTEMPTS must be greater than zero" >&2
	exit 2
fi
if [[ ! "$random_seed" =~ ^[0-9]+$ ]] || ((10#$random_seed > 2147483647)); then
	echo "E2E_RANDOM_SEED must be an integer from 0 through 2147483647" >&2
	exit 2
fi
random_seed=$((10#$random_seed))
suite_started_ms="$(e2e_now_ms)"

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
	if [[ "${E2E_INCLUDE_PRIVILEGED:-}" != "1" ]]; then
		case_find_args+=( '!' -exec test -e '{}/requires-privileged' ';' )
	fi
	if [[ "${E2E_EXCLUDE_HEAVY:-}" == "1" ]]; then
		case_find_args+=( '!' -exec test -e '{}/heavy' ';' )
	fi
	mapfile -t cases < <(find "${case_find_args[@]}" -printf '%f\n' | sort)
fi

if ((${#cases[@]} == 0)); then
	echo "no e2e cases found" >&2
	exit 1
fi

e2e_shuffle_cases "$random_seed" cases
echo "==> e2e: random seed $random_seed"
exclusive_count=0
for case_name in "${cases[@]}"; do
	if e2e_case_is_exclusive "$repo_root/e2e/cases/$case_name"; then
		((exclusive_count += 1))
	fi
done
echo "==> e2e: ${#cases[@]} cases, $jobs jobs, $exclusive_count exclusive"

declare -A built_images=()
case_images=()
case_coverage_dirs=()
source_input_hash="$(hash_image_inputs)"

for case_index in "${!cases[@]}"; do
	case_name="${cases[$case_index]}"
	echo "==> e2e: preparing $case_name"
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
	case_images[case_index]="$case_image"
	if [[ "$coverage_enabled" == "1" ]]; then
		case_coverage_dirs[case_index]="$coverage_dir/raw/$case_name"
		mkdir -p "${case_coverage_dirs[case_index]}"
		coverage_inputs+=("${case_coverage_dirs[case_index]}")
	fi
done

run_dir="$(mktemp -d "${TMPDIR:-/tmp}/almagest-e2e-run.XXXXXX")"
declare -A running_cases=()
case_logs=()
case_cidfiles=()
case_started_ms=()
case_finished_ms=()
case_durations_ms=()
case_privileges=()

cleanup() {
	local exit_status="$?"
	local cidfile
	local container_id
	local pid

	trap - EXIT INT TERM
	for pid in "${!running_cases[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	for pid in "${!running_cases[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
	for cidfile in "$run_dir"/*.cid; do
		[[ -f "$cidfile" ]] || continue
		container_id="$(<"$cidfile")"
		[[ -n "$container_id" ]] || continue
		docker rm --force "$container_id" >/dev/null 2>&1 || true
	done
	rm -rf "$run_dir"
	exit "$exit_status"
}

trap cleanup EXIT
trap 'exit 130' INT TERM

run_case() {
	local case_index="$1"
	local case_name="${cases[$case_index]}"
	local case_image="${case_images[$case_index]}"
	local case_runner="e2e/cases/$case_name/docker-run.sh"
	local docker_run_args=()
	local host_pid_namespace=0
	e2e_add_case_privileges "e2e/cases/$case_name" docker_run_args
	if [[ -f "e2e/cases/$case_name/requires-host-pid" ]]; then
		host_pid_namespace=1
		docker_run_args+=(--pid host)
	fi
	if [[ "$coverage_enabled" == "1" ]]; then
		docker_run_args+=(
			-e GOCOVERDIR=/e2e-coverage
			-v "${case_coverage_dirs[$case_index]}:/e2e-coverage"
		)
	fi
	if [[ -x "$case_runner" ]]; then
		E2E_CASE_IMAGE="$case_image" \
			E2E_CASE_CIDFILE="${case_cidfiles[$case_index]}" \
			E2E_CASE_COVERAGE_DIR="${case_coverage_dirs[$case_index]:-}" \
			E2E_CASE_COVERAGE="$coverage_enabled" \
			E2E_CASE_HOST_PID="$host_pid_namespace" \
			"$case_runner" "$case_name"
		return
	fi
	# shellcheck disable=SC2086
	docker run --rm --cidfile "${case_cidfiles[$case_index]}" \
		${E2E_DOCKER_RUN_ARGS:-} "${docker_run_args[@]}" "$case_image" "$case_name"
}

pending_cases=("${!cases[@]}")
failure_count=0
failed_cases=()
exclusive_case_running=0

while ((${#pending_cases[@]} > 0 || ${#running_cases[@]} > 0)); do
	while ((${#pending_cases[@]} > 0 && ${#running_cases[@]} < jobs)); do
		if ! pending_index="$(e2e_find_startable_case \
			"$repo_root/e2e/cases" cases pending_cases \
			"${#running_cases[@]}" "$exclusive_case_running")"; then
			break
		fi
		case_index="${pending_cases[$pending_index]}"
		case_name="${cases[$case_index]}"
		case_logs[case_index]="$run_dir/$case_index.log"
		case_cidfiles[case_index]="$run_dir/$case_index.cid"
		case_finished_ms[case_index]="$run_dir/$case_index.finished-ms"
		case_privileges[case_index]="$(e2e_case_privilege "$repo_root/e2e/cases/$case_name")"
		case_started_ms[case_index]="$(e2e_now_ms)"
		echo "==> e2e: starting $case_name [${case_privileges[$case_index]}]"
		(
			case_status=0
			run_case "$case_index" || case_status=$?
			e2e_now_ms >"${case_finished_ms[$case_index]}"
			exit "$case_status"
		) >"${case_logs[$case_index]}" 2>&1 &
		running_cases[$!]="$case_index"
		if e2e_case_is_exclusive "$repo_root/e2e/cases/$case_name"; then
			exclusive_case_running=1
		fi
		unset 'pending_cases[pending_index]'
	done
	if ((${#running_cases[@]} == 0)); then
		echo "e2e scheduler could not start a pending case" >&2
		exit 2
	fi

	completed_pid=""
	case_status=0
	if wait -n -p completed_pid "${!running_cases[@]}"; then
		case_status=0
	else
		case_status=$?
	fi
	completed_index="${running_cases[$completed_pid]}"
	case_name="${cases[$completed_index]}"
	if e2e_case_is_exclusive "$repo_root/e2e/cases/$case_name"; then
		exclusive_case_running=0
	fi
	completed_ms="$(<"${case_finished_ms[$completed_index]}")"
	case_duration_ms=$((completed_ms - case_started_ms[completed_index]))
	case_durations_ms[completed_index]="$case_duration_ms"
	case_duration="$(e2e_format_duration "$case_duration_ms")"
	if ((case_status == 0)); then
		echo "==> e2e: PASS $case_name ($case_duration)"
	else
		cat "${case_logs[$completed_index]}" >&2
		echo "==> e2e: FAIL $case_name (exit $case_status, $case_duration)" >&2
		failed_cases+=("$case_name")
		((failure_count += 1))
	fi
	unset 'running_cases[$completed_pid]'
done

if ((failure_count > 0)); then
	echo "e2e failed: ${failed_cases[*]}" >&2
elif [[ "$coverage_enabled" == "1" ]]; then
	input_dirs="$(IFS=,; printf '%s' "${coverage_inputs[*]}")"
	go tool covdata percent -i="$input_dirs" | tee "$coverage_dir/summary.txt"
	go tool covdata textfmt -i="$input_dirs" -o "$coverage_dir/coverage.out"
	go tool cover -func="$coverage_dir/coverage.out" >"$coverage_dir/functions.txt"
	tail -1 "$coverage_dir/functions.txt" | tee "$coverage_dir/total.txt"
fi

suite_duration_ms=$(($(e2e_now_ms) - suite_started_ms))
echo "==> e2e: completed ${#cases[@]} cases in $(e2e_format_duration "$suite_duration_ms")"
report_count="$slowest_count"
if ((report_count > ${#cases[@]})); then
	report_count="${#cases[@]}"
fi
echo "==> e2e: slowest $report_count cases"
while IFS=$'\t' read -r case_duration_ms case_index; do
	case_name="${cases[$case_index]}"
	printf '    %s [%s] %s\n' \
		"$case_name" "${case_privileges[$case_index]}" "$(e2e_format_duration "$case_duration_ms")"
done < <(
	for case_index in "${!cases[@]}"; do
		printf '%d\t%d\n' "${case_durations_ms[$case_index]}" "$case_index"
	done | sort -rn -k1,1 | sed -n "1,${report_count}p"
)

if ((failure_count > 0)); then
	exit 1
fi
