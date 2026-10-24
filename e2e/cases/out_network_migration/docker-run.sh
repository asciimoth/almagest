#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # Shared host-side Docker arguments.
source "$(dirname -- "${BASH_SOURCE[0]}")/../../lib/case_scheduler.sh"

case_name="${1:?case name is required}"
: "${E2E_CASE_IMAGE:?}"
: "${E2E_CASE_CIDFILE:?}"

run_id="${case_name//[^a-zA-Z0-9]/-}-$$-$RANDOM"
resource_label="io.almagest.e2e.run=${E2E_RUN_ID:-$run_id}"
container_names=(
	"almagest-$run_id-ipv6-enabled"
	"almagest-$run_id-ipv6-disabled"
)

cleanup() {
	local status="$?"
	local container_name

	trap - EXIT INT TERM
	for container_name in "${container_names[@]}"; do
		docker rm --force "$container_name" >/dev/null 2>&1 || true
	done
	rm -f "$E2E_CASE_CIDFILE"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

tmpfs_args=()
e2e_add_tmpfs tmpfs_args

coverage_args=()
if [[ "${E2E_CASE_COVERAGE:-0}" == "1" ]]; then
	coverage_args=(
		-e GOCOVERDIR=/e2e-coverage
		-v "${E2E_CASE_COVERAGE_DIR:?}:/e2e-coverage"
	)
fi

run_profile() {
	local profile="${1:?IPv6 profile is required}"
	local container_name="${2:?container name is required}"
	shift 2
	local profile_args=("$@")

	rm -f "$E2E_CASE_CIDFILE"
	printf '[%s] running IPv6 profile=%s\n' "$case_name" "$profile" >&2
	# shellcheck disable=SC2086 # Extra Docker arguments intentionally use shell words.
	docker run --rm --name "$container_name" \
		--cidfile "$E2E_CASE_CIDFILE" \
		--label "$resource_label" \
		--privileged --device /dev/net/tun \
		-e "ALMAGEST_E2E_IPV6_MODE=$profile" \
		${E2E_DOCKER_RUN_ARGS:-} \
		"${profile_args[@]}" \
		"${tmpfs_args[@]}" \
		"${coverage_args[@]}" \
		"$E2E_CASE_IMAGE" "$case_name"
}

run_profile enabled "${container_names[0]}"
run_profile disabled "${container_names[1]}" \
	--sysctl net.ipv6.conf.all.disable_ipv6=1 \
	--sysctl net.ipv6.conf.default.disable_ipv6=1
