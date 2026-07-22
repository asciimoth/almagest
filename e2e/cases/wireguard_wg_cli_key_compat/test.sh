#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh
source ./wg-keys.env

client_menu="/tmp/e2e-wireguard-wg-cli-client-menu.json"
server_menu="/tmp/e2e-wireguard-wg-cli-server-menu.json"
http_client_menu="/tmp/e2e-wireguard-wg-cli-http-client-menu.json"

menu_snapshot() {
	local node="$1"
	local path="$2"
	cli graph menu --node "$node" >"$path"
}

menu_field() {
	local path="$1"
	local block="$2"
	local field="$3"
	jq -er --arg block "$block" --arg field "$field" '
		.blocks[]
		| select(.id == $block)
		| .items[]
		| select(.id == $field)
		| (.value // "")
	' "$path"
}

peer_field() {
	local path="$1"
	local field="$2"
	jq -er --arg field "$field" '
		.blocks[]
		| select(.id == "peers")
		| .items[]
		| select(.id == "peers")
		| .elements[0].items[]
		| select(.id == $field)
		| (.value // "")
	' "$path"
}

client_status() {
	jq -er '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == "status")
		| .text
	' "$http_client_menu"
}

client_field() {
	local field="$1"
	jq -er --arg field "$field" '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == $field)
		| (.value // "")
	' "$http_client_menu"
}

send_client_request() {
	local node="$1"
	cli graph click-menu-button --node "$node" --block request --button send \
		>/tmp/e2e-wireguard-wg-cli-click.json
}

assert_client_request_works() {
	local node="$1"
	local deadline=$((SECONDS + 45))
	local request_deadline
	local status=""
	local err=""
	local response=""

	while ((SECONDS <= deadline)); do
		menu_snapshot "$node" "$http_client_menu"
		status="$(client_status)"
		if [[ "$status" != "Status: running..." ]]; then
			send_client_request "$node"
		fi

		request_deadline=$((SECONDS + 20))
		while ((SECONDS <= request_deadline)); do
			menu_snapshot "$node" "$http_client_menu"
			status="$(client_status)"
			err="$(client_field error)"
			response="$(client_field response)"
			if [[ "$status" == "Status: 200 OK" && -z "$err" && "$response" == "wg cli key compatibility" ]]; then
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				break
			fi
			sleep 0.25
		done

		sleep 0.25
	done

	fail "HTTP client did not receive WireGuard response; status=$status error=$err response=$response"
}

assert_key_equals() {
	local label="$1"
	local got="$2"
	local want="$3"
	if [[ "$got" != "$want" ]]; then
		fail "$label = $got, want $want"
	fi
}

start_daemon

wait_graph_node "WireGuard CLI Client" || fail "client WireGuard node missing"
wait_graph_node "WireGuard CLI Server" || fail "server WireGuard node missing"
wait_graph_node "VTun CLI Client" || fail "client VTun node missing"
wait_graph_node "VTun CLI Server" || fail "server VTun node missing"
wait_graph_node "HTTP CLI Server" || fail "HTTP server node missing"
wait_graph_node "HTTP CLI Client" || fail "HTTP client node missing"
wait_graph_node "SocksServer CLI" || fail "SOCKS server node missing"

client_wg="$(node_id_by_name "WireGuard CLI Client")"
server_wg="$(node_id_by_name "WireGuard CLI Server")"
client_vtun="$(node_id_by_name "VTun CLI Client")"
server_vtun="$(node_id_by_name "VTun CLI Server")"
http_client="$(node_id_by_name "HTTP CLI Client")"

wait_log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:1082" \
	|| fail "SOCKS server did not start listening"
wait_log_contains "almagest/HttpServer" "listening on 10.199.20.1:18081" \
	|| fail "HTTP server did not start listening"

wait_node_label "$client_wg" up || fail "client WireGuard node did not become up"
wait_node_label "$server_wg" up || fail "server WireGuard node did not become up"
wait_node_label "$client_vtun" wg-cli-client || fail "client VTun did not become ready"
wait_node_label "$server_vtun" wg-cli-server || fail "server VTun did not become ready"

menu_snapshot "$client_wg" "$client_menu"
menu_snapshot "$server_wg" "$server_menu"

client_public_from_wg="$(printf '%s\n' "$CLIENT_PRIVATE" | wg pubkey)"
server_public_from_wg="$(printf '%s\n' "$SERVER_PRIVATE_PADDED" | wg pubkey)"
assert_key_equals "client status public key" "$(menu_field "$client_menu" status publicKey)" "$client_public_from_wg"
assert_key_equals "server status public key" "$(menu_field "$server_menu" status publicKey)" "$server_public_from_wg"
assert_key_equals "client status public key from setup" "$client_public_from_wg" "$CLIENT_PUBLIC"
assert_key_equals "server status public key from setup" "$server_public_from_wg" "$SERVER_PUBLIC_PADDED"

assert_key_equals "server private key secret name" "$(menu_field "$server_menu" config privateKeySecret)" "wireguard-cli-server-private-key"
assert_key_equals "client peer public key normalized from unpadded input" "$(peer_field "$client_menu" publicKey)" "$SERVER_PUBLIC_PADDED"
assert_key_equals "client peer preshared key normalized from unpadded input" "$(peer_field "$client_menu" presharedKey)" "$PRESHARED_KEY_PADDED"
assert_key_equals "server peer preshared key normalized from unpadded input" "$(peer_field "$server_menu" presharedKey)" "$PRESHARED_KEY_PADDED"

assert_client_request_works "$http_client"
wait_node_label "$http_client" "200 OK" || fail "HTTP client node did not report 200 OK"

wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the WireGuard request"

log "case passed"
