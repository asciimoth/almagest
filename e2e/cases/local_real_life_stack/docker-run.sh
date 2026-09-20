#!/usr/bin/env bash
set -euo pipefail

case_name="${1:?case name is required}"
: "${E2E_CASE_IMAGE:?}"
: "${E2E_CASE_CIDFILE:?}"

run_id="${case_name//[^a-zA-Z0-9]/-}-$$-$RANDOM"
network_name="almagest-$run_id"
internet_network_name="almagest-internet-$run_id"
volume_name="almagest-$run_id"
amnesia_name="almagest-amnesia-$run_id"
headscale_name="almagest-headscale-$run_id"
public_name="almagest-public-$run_id"
example_name="almagest-example-$run_id"
main_name="almagest-main-$run_id"

service_logs() {
	local name
	for name in "$public_name" "$example_name" "$amnesia_name" "$headscale_name"; do
		if docker container inspect "$name" >/dev/null 2>&1; then
			echo "==> e2e: $name logs" >&2
			docker logs --tail 300 "$name" >&2 || true
		fi
	done
}

service_diagnostics() {
	if docker container inspect "$headscale_name" >/dev/null 2>&1; then
		echo "==> e2e: tailnet peer menus" >&2
		docker exec "$headscale_name" almagest-cli \
			--url unix:///tmp/almagest-headscale-service/peer.sock \
			graph menu --name "Tailscale Peer" \
			| jq '{status: [.blocks[] | select(.id == "status") | .items[] | select(.id == "state" or .id == "localEndpoints" or .id == "peers" or .id == "error") | {id, value}]}' >&2 || true
		docker exec "$headscale_name" almagest-cli \
			--url unix:///tmp/almagest-headscale-service/peer.sock \
			graph menu --name "WireGuard Peer" \
			| jq '{status: [.blocks[] | select(.id == "status") | .items[] | select(.id == "state" or .id == "connectedPeers" or .id == "rxBytes" or .id == "txBytes" or .id == "latestHandshake" or .id == "error") | {id, value}]}' >&2 || true
		docker exec "$headscale_name" sh -c \
			'grep -E "handshake|Handshake|Packet with invalid|disallowed source" /tmp/almagest-headscale-service/peer.log | tail -100' >&2 || true
		docker exec "$headscale_name" tail -120 \
			/tmp/almagest-headscale-service/tailscale-packets.log >&2 || true
	fi
	if docker container inspect "$amnesia_name" >/dev/null 2>&1; then
		echo "==> e2e: Amnesia service tunnel log" >&2
		docker exec "$amnesia_name" nft list table inet stack_forward >&2 || true
		docker exec "$amnesia_name" sh -c \
			'grep -E "disallowed source|invalid initiation|Received handshake" /tmp/almagest-amnesia-service/amneziawg-go.log | tail -40' >&2 || true
		docker exec "$amnesia_name" sh -c \
			'tail -n 120 -- /tmp/almagest-amnesia-service/tailscale-*-packets.log' >&2 || true
	fi
	if docker container inspect "$main_name" >/dev/null 2>&1; then
		echo "==> e2e: main nested Tailscale packets" >&2
		docker cp "$main_name:/tmp/almagest-e2e/local_real_life_stack/tailscale-packets.log" - 2>/dev/null \
			| tar -xO 2>/dev/null | tail -120 >&2 || true
	fi
}

cleanup() {
	local status="$?"
	trap - EXIT INT TERM
	if ((status != 0)); then
		service_diagnostics
		service_logs
	fi
	docker rm --force "$main_name" "$headscale_name" "$amnesia_name" \
		"$example_name" "$public_name" >/dev/null 2>&1 || true
	docker network rm "$internet_network_name" "$network_name" >/dev/null 2>&1 || true
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
		if ! docker container inspect "$container" >/dev/null 2>&1; then
			echo "$description container disappeared" >&2
			return 1
		fi
		if [[ "$(docker container inspect --format '{{.State.Running}}' "$container")" != "true" ]]; then
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
	local network="${2:-$network_name}"
	docker container inspect --format "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" "$container"
}

coverage_args=()
if [[ "${E2E_CASE_COVERAGE:-0}" == "1" ]]; then
	coverage_args=(
		-e GOCOVERDIR=/e2e-coverage
		-v "${E2E_CASE_COVERAGE_DIR:?}:/e2e-coverage"
	)
fi

docker network create \
	--label io.almagest.e2e.case="$case_name" \
	"$network_name" >/dev/null
docker network create \
	--label io.almagest.e2e.case="$case_name" \
	"$internet_network_name" >/dev/null
docker volume create \
	--label io.almagest.e2e.case="$case_name" \
	"$volume_name" >/dev/null

docker run --detach --name "$public_name" \
	--network "$internet_network_name" \
	-e E2E_ORIGIN_NAME=public \
	--entrypoint "/e2e/cases/$case_name/services/origin.sh" \
	"$E2E_CASE_IMAGE" >/dev/null
wait_container_file "$public_name" /tmp/origin-ready "public origin"
public_ip="$(container_ip "$public_name" "$internet_network_name")"
[[ -n "$public_ip" ]] || { echo "public origin has no Docker address" >&2; exit 1; }

docker run --detach --name "$example_name" \
	--network "$internet_network_name" \
	-e E2E_ORIGIN_NAME=example \
	--entrypoint "/e2e/cases/$case_name/services/origin.sh" \
	"$E2E_CASE_IMAGE" >/dev/null
wait_container_file "$example_name" /tmp/origin-ready "example origin"
example_ip="$(container_ip "$example_name" "$internet_network_name")"
[[ -n "$example_ip" ]] || { echo "example origin has no Docker address" >&2; exit 1; }

internet_gateway_ip="$(docker network inspect --format '{{(index .IPAM.Config 0).Gateway}}' "$internet_network_name")"
[[ -n "$internet_gateway_ip" ]] || { echo "internet network has no gateway address" >&2; exit 1; }

docker create --name "$amnesia_name" \
	--network "$network_name" \
	--privileged --device /dev/net/tun \
	-e "E2E_PUBLIC_IP=$public_ip" \
	-e "E2E_EXAMPLE_IP=$example_ip" \
	-v "$volume_name:/stack-shared" \
	--entrypoint "/e2e/cases/$case_name/services/amnesia.sh" \
	"$E2E_CASE_IMAGE" >/dev/null
docker network connect "$internet_network_name" "$amnesia_name"
docker start "$amnesia_name" >/dev/null
wait_container_file "$amnesia_name" /stack-shared/amnesia-ready "Amnesia service"
amnesia_ip="$(container_ip "$amnesia_name")"
amnesia_internet_ip="$(container_ip "$amnesia_name" "$internet_network_name")"
[[ -n "$amnesia_ip" && -n "$amnesia_internet_ip" ]] \
	|| { echo "Amnesia service has incomplete Docker addresses" >&2; exit 1; }

docker run --detach --name "$headscale_name" \
	--network "container:$amnesia_name" \
	--cap-add NET_ADMIN \
	-e "E2E_AMNESIA_IP=$amnesia_ip" \
	-v "$volume_name:/stack-shared" \
	"${coverage_args[@]}" \
	--entrypoint "/e2e/cases/$case_name/services/headscale.sh" \
	"$E2E_CASE_IMAGE" >/dev/null
wait_container_file "$headscale_name" /stack-shared/headscale-ready "Headscale service"
# Headscale is a separate container, but it is placed behind the Amnesia
# gateway by sharing that container's Docker network namespace.
headscale_ip="$amnesia_ip"

# The main case is privileged because DefaultTun changes only this container's
# network namespace. No host network namespace is shared by any container.
# shellcheck disable=SC2086
docker create --name "$main_name" \
	--cidfile "$E2E_CASE_CIDFILE" \
	--network "$network_name" \
	--privileged --device /dev/net/tun \
	-e "E2E_AMNESIA_IP=$amnesia_ip" \
	-e "E2E_AMNESIA_INTERNET_IP=$amnesia_internet_ip" \
	-e "E2E_INTERNET_GATEWAY_IP=$internet_gateway_ip" \
	-e "E2E_HEADSCALE_IP=$headscale_ip" \
	-e "E2E_PUBLIC_IP=$public_ip" \
	-e "E2E_EXAMPLE_IP=$example_ip" \
	-v "$volume_name:/stack-shared" \
	${E2E_DOCKER_RUN_ARGS:-} \
	"${coverage_args[@]}" \
	"$E2E_CASE_IMAGE" "$case_name" >/dev/null
docker network connect "$internet_network_name" "$main_name"
docker start --attach "$main_name" &
main_pid="$!"

status=0
wait "$main_pid" || status=$?
exit "$status"
