#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

client_menu="/tmp/e2e-wireguard-socks-client-menu.json"

client_result_snapshot() {
	local node="$1"
	cli graph menu --node "$node" >"$client_menu"
}

client_status() {
	jq -er '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == "status")
		| .text
	' "$client_menu"
}

client_field() {
	local field="$1"
	jq -er --arg field "$field" '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == $field)
		| (.value // "")
	' "$client_menu"
}

send_client_request() {
	local node="$1"
	cli graph click-menu-button --node "$node" --block request --button send \
		>/tmp/e2e-wireguard-socks-click.json
}

assert_client_request_works() {
	local node="$1"
	local deadline=$((SECONDS + 45))
	local request_deadline
	local status
	local err
	local response

	while ((SECONDS <= deadline)); do
		client_result_snapshot "$node"
		status="$(client_status)"
		if [[ "$status" != "Status: running..." ]]; then
			send_client_request "$node"
		fi

		request_deadline=$((SECONDS + 20))
		while ((SECONDS <= request_deadline)); do
			client_result_snapshot "$node"
			status="$(client_status)"
			err="$(client_field error)"
			response="$(client_field response)"
			if [[ "$status" == "Status: 200 OK" && -z "$err" && "$response" == "Debug http server response" ]]; then
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				break
			fi
			sleep 0.25
		done

		sleep 0.25
	done

	fail "HTTP client did not receive WireGuard-over-SOCKS response; status=$status error=$err response=$response"
}

start_daemon

wait_graph_node "WireGuard Client" || fail "WireGuard Client node missing"
wait_graph_node "WireGuard Server" || fail "WireGuard Server node missing"
wait_graph_node "VTun Client" || fail "VTun Client node missing"
wait_graph_node "VTun Server" || fail "VTun Server node missing"
wait_graph_node "HTTP Server" || fail "HTTP Server node missing"
wait_graph_node "HTTP Client" || fail "HTTP Client node missing"
wait_graph_node "SocksServer 70" || fail "SocksServer node missing"
wait_graph_node "SocksClient 56" || fail "client-side SocksClient node missing"
wait_graph_node "SocksClient 86" || fail "server-side SocksClient node missing"

client_wg="$(node_id_by_name "WireGuard Client")"
server_wg="$(node_id_by_name "WireGuard Server")"
client_vtun="$(node_id_by_name "VTun Client")"
server_vtun="$(node_id_by_name "VTun Server")"
http_client="$(node_id_by_name "HTTP Client")"

wait_log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:1081" \
	|| fail "SOCKS server did not start listening"
wait_log_contains "almagest/HttpServer" "listening on 10.198.20.1:18080" \
	|| fail "HTTP server did not start listening"

wait_node_label "$client_wg" up || fail "client WireGuard node did not become up"
wait_node_label "$server_wg" up || fail "server WireGuard node did not become up"
wait_node_label "$client_vtun" wg-demo-client || fail "client VTun did not become ready"
wait_node_label "$server_vtun" wg-demo-server || fail "server VTun did not become ready"

assert_client_request_works "$http_client"
wait_node_label "$http_client" "200 OK" || fail "HTTP client node did not report 200 OK"

wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the WireGuard request"

log "case passed"
