#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # Shared host-side Docker arguments.
source "$(dirname -- "${BASH_SOURCE[0]}")/../../lib/case_scheduler.sh"
tmpfs_args=()
e2e_add_tmpfs tmpfs_args

case_name="${1:?case name is required}"
: "${E2E_CASE_IMAGE:?}"
: "${E2E_CASE_CIDFILE:?}"

run_id="${case_name//[^a-zA-Z0-9]/-}-$$-$RANDOM"
resource_label="io.almagest.e2e.run=${E2E_RUN_ID:-$run_id}"
network_name="almagest-$run_id"
container_name="almagest-$run_id"

cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	docker rm --force "$container_name" >/dev/null 2>&1 || true
	docker network rm "$network_name" >/dev/null 2>&1 || true
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

# The internal network prevents accidental use of the public Tor network.
# Chutney supplies all directory authorities and relays inside the container.
docker network create --internal --label "$resource_label" \
	--label io.almagest.e2e.case="$case_name" \
	"$network_name" >/dev/null

# shellcheck disable=SC2086
docker run "${tmpfs_args[@]}" --rm --label "$resource_label" --name "$container_name" \
	--cidfile "$E2E_CASE_CIDFILE" \
	--network "$network_name" \
	--user root \
	--privileged --device /dev/net/tun \
	${E2E_DOCKER_RUN_ARGS:-} \
	"${coverage_args[@]}" \
	"$E2E_CASE_IMAGE" "$case_name"
