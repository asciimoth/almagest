#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091 # Shared host-side Docker arguments.
source "$(dirname -- "${BASH_SOURCE[0]}")/../../lib/case_scheduler.sh"
tmpfs_args=()
e2e_add_tmpfs tmpfs_args
volume_args=()
if [[ ${ALMAGEST_TEST_TMPFS:-1} != 0 ]]; then
	volume_args=(--opt type=tmpfs --opt device=tmpfs --opt o=size=128m)
fi

case_name="${1:?case name is required}"
: "${E2E_CASE_IMAGE:?}"
: "${E2E_CASE_CIDFILE:?}"

run_id="${case_name//[^a-zA-Z0-9]/-}-$$-$RANDOM"
resource_label="io.almagest.e2e.run=${E2E_RUN_ID:-$run_id}"
network_name="almagest-$run_id"
volume_name="almagest-$run_id"
headscale_name="almagest-headscale-$run_id"
public_name="almagest-public-$run_id"
example_name="almagest-example-$run_id"
main_name="almagest-main-$run_id"

# shellcheck disable=SC2329 # Invoked through cleanup, which is an EXIT trap.
service_logs() {
	local name
	for name in "$public_name" "$example_name" "$headscale_name"; do
		if docker container inspect "$name" >/dev/null 2>&1; then
			echo "==> e2e: $name logs" >&2
			docker logs --tail 300 "$name" >&2 || true
		fi
	done
}

# shellcheck disable=SC2329 # Invoked through cleanup, which is an EXIT trap.
service_diagnostics() {
	if docker container inspect "$headscale_name" >/dev/null 2>&1; then
		echo "==> e2e: tailnet peer menus" >&2
		docker exec "$headscale_name" almagest-cli \
			--url unix:///tmp/almagest-headscale-service/peer.sock \
			graph menu --name "Tailscale Peer" >&2 || true
		docker exec "$headscale_name" almagest-cli \
			--url unix:///tmp/almagest-headscale-service/peer.sock \
			graph menu --name "WireGuard Peer" >&2 || true
		docker exec "$headscale_name" tail -120 \
			/tmp/almagest-headscale-service/tailscale-packets.log >&2 || true
	fi
	if docker container inspect "$main_name" >/dev/null 2>&1; then
		echo "==> e2e: main Spoofer and nested Tailscale packets" >&2
		docker cp "$main_name:/tmp/almagest-e2e/$case_name/tailscale-packets.log" - 2>/dev/null \
			| tar -xO 2>/dev/null | tail -120 >&2 || true
	fi
}

# shellcheck disable=SC2329 # Invoked through the EXIT trap.
cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	if ((status != 0)); then
		service_diagnostics
		service_logs
	fi
	docker rm --force "$main_name" "$headscale_name" \
		"$example_name" "$public_name" >/dev/null 2>&1 || true
	docker network rm "$network_name" >/dev/null 2>&1 || true
	docker volume rm "$volume_name" >/dev/null 2>&1 || true
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

wait_container_file() {
	local container="$1"
	local path="$2"
	local description="$3"
	local deadline=$((SECONDS + 120))
	while ((SECONDS <= deadline)); do
		if [[ "$(docker container inspect --format '{{.State.Running}}' "$container" 2>/dev/null || true)" != "true" ]]; then
			echo "$description container stopped" >&2
			return 1
		fi
		if docker exec "$container" test -f "$path"; then
			return 0
		fi
		sleep 0.25
	done
	echo "timed out waiting for $description" >&2
	return 1
}

container_ip() {
	local container="$1"
	docker container inspect --format \
		"{{with index .NetworkSettings.Networks \"$network_name\"}}{{.IPAddress}}{{end}}" \
		"$container"
}

coverage_args=()
if [[ "${E2E_CASE_COVERAGE:-0}" == "1" ]]; then
	coverage_args=(
		-e GOCOVERDIR=/e2e-coverage
		-v "${E2E_CASE_COVERAGE_DIR:?}:/e2e-coverage"
	)
fi

pid_args=()
if [[ "${E2E_CASE_HOST_PID:-0}" == "1" ]]; then
	pid_args=(--pid host)
fi

docker network create --label "$resource_label" --label io.almagest.e2e.case="$case_name" \
	"$network_name" >/dev/null
docker volume create "${volume_args[@]}" --label "$resource_label" --label io.almagest.e2e.case="$case_name" \
	"$volume_name" >/dev/null

docker run "${tmpfs_args[@]}" --label "$resource_label" --detach --name "$public_name" --network "$network_name" \
	-e E2E_ORIGIN_NAME=public \
	--entrypoint /e2e/cases/local_real_life_stack/services/origin.sh \
	"$E2E_CASE_IMAGE" >/dev/null
wait_container_file "$public_name" /tmp/origin-ready "public origin"
public_ip="$(container_ip "$public_name")"

docker run "${tmpfs_args[@]}" --label "$resource_label" --detach --name "$example_name" --network "$network_name" \
	-e E2E_ORIGIN_NAME=example \
	--entrypoint /e2e/cases/local_real_life_stack/services/origin.sh \
	"$E2E_CASE_IMAGE" >/dev/null
wait_container_file "$example_name" /tmp/origin-ready "example origin"
example_ip="$(container_ip "$example_name")"

gateway_ip="$(docker network inspect --format '{{(index .IPAM.Config 0).Gateway}}' "$network_name")"
[[ -n "$public_ip" && -n "$example_ip" && -n "$gateway_ip" ]]

# The main container uses stable service addresses on both simulated physical
# networks. Its router namespace translates these addresses to the Docker
# service addresses.
virtual_public_ip="198.18.100.10"
virtual_example_ip="198.18.100.11"
virtual_headscale_ip="198.18.100.12"

docker run "${tmpfs_args[@]}" --label "$resource_label" --detach --name "$headscale_name" --network "$network_name" \
	--cap-add NET_ADMIN \
	-e E2E_HEADSCALE_STANDALONE=1 \
	-e ALMAGEST_TAILSCALE_E2E_REJECT_AFTER_SECONDS=20 \
	-e "E2E_PUBLIC_IP=$virtual_public_ip" \
	-e "E2E_EXAMPLE_IP=$virtual_example_ip" \
	-v "$volume_name:/stack-shared" \
	"${coverage_args[@]}" \
	--entrypoint /e2e/cases/local_real_life_stack/services/headscale.sh \
	"$E2E_CASE_IMAGE" >/dev/null
wait_container_file "$headscale_name" /stack-shared/headscale-ready "Headscale service"
headscale_ip="$(container_ip "$headscale_name")"
[[ -n "$headscale_ip" ]]

# The main container keeps a private network namespace. A host PID namespace
# is added only when the case marker requests it for p-mark identity matching.
# shellcheck disable=SC2086
docker create "${tmpfs_args[@]}" --label "$resource_label" --name "$main_name" \
	--cidfile "$E2E_CASE_CIDFILE" \
	--network "$network_name" \
	--privileged --device /dev/net/tun \
	-e "E2E_INTERNET_GATEWAY_IP=$gateway_ip" \
	-e "E2E_BACKEND_HEADSCALE_IP=$headscale_ip" \
	-e "E2E_BACKEND_PUBLIC_IP=$public_ip" \
	-e "E2E_BACKEND_EXAMPLE_IP=$example_ip" \
	-e "E2E_HEADSCALE_IP=$virtual_headscale_ip" \
	-e "E2E_PUBLIC_IP=$virtual_public_ip" \
	-e "E2E_EXAMPLE_IP=$virtual_example_ip" \
	-e ALMAGEST_TAILSCALE_E2E_REJECT_AFTER_SECONDS=20 \
	-v "$volume_name:/stack-shared" \
	${E2E_DOCKER_RUN_ARGS:-} \
	"${pid_args[@]}" \
	"${coverage_args[@]}" \
	"$E2E_CASE_IMAGE" "$case_name" >/dev/null
docker start --attach "$main_name" &
main_pid="$!"

status=0
wait "$main_pid" || status=$?
exit "$status"
