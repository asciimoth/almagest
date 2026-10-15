#!/usr/bin/env bash
set -euo pipefail

case_name="${1:?case name is required}"
: "${E2E_CASE_IMAGE:?}"
: "${E2E_CASE_CIDFILE:?}"

run_id="${case_name//[^a-zA-Z0-9]/-}-$$-$RANDOM"
resource_label="io.almagest.e2e.run=${E2E_RUN_ID:-$run_id}"
container_name="almagest-$run_id"

cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	docker rm --force "$container_name" >/dev/null 2>&1 || true
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

coverage_args=()
if [[ "${E2E_CASE_COVERAGE:-0}" == "1" ]]; then
	coverage_args=(
		-e GOCOVERDIR=/e2e-coverage
		-v "${E2E_CASE_COVERAGE_DIR:?}:/e2e-coverage"
	)
fi

if [[ -n "${E2E_DOCKER_RUN_ARGS:-}" ]]; then
	echo "linux_adaptive_isolation rejects E2E_DOCKER_RUN_ARGS to preserve its container boundary" >&2
	exit 2
fi

# NET_ADMIN is only launch input for the ambient-capability scenario. The
# payload uses a private Docker network namespace with no configured network.
docker run --rm --label "$resource_label" --name "$container_name" \
	--cidfile "$E2E_CASE_CIDFILE" \
	--network none \
	--cap-add NET_ADMIN \
	--user root \
	"${coverage_args[@]}" \
	"$E2E_CASE_IMAGE" "$case_name"
