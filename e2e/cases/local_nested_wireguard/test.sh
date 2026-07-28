#!/usr/bin/env bash
set -euo pipefail

: "${ALMAGEST_E2E_TIMEOUT:=45}"
source /e2e/lib/common.sh

http_client_menu="/tmp/e2e-local-nested-wireguard-http-client-menu.json"
udp_client_menu="/tmp/e2e-local-nested-wireguard-udp-client-menu.json"

menu_snapshot() {
	local node="$1"
	local path="$2"
	cli graph menu --node "$node" >"$path"
}

menu_status() {
	local path="$1"
	jq -er '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == "status")
		| .text
	' "$path"
}

menu_field() {
	local path="$1"
	local field="$2"
	jq -er --arg field "$field" '
		.blocks[]
		| select(.id == "result")
		| .items[]
		| select(.id == $field)
		| (.value // "")
	' "$path"
}

click_menu_button() {
	local node="$1"
	local block="$2"
	local button="$3"
	cli graph click-menu-button --node "$node" --block "$block" --button "$button" \
		>/tmp/e2e-local-nested-wireguard-click.json
}

assert_http_request_works() {
	local node="$1"
	local deadline=$((SECONDS + 90))
	local request_deadline
	local status=""
	local err=""
	local response=""

	while ((SECONDS <= deadline)); do
		menu_snapshot "$node" "$http_client_menu"
		status="$(menu_status "$http_client_menu")"
		if [[ "$status" != "Status: running..." ]]; then
			click_menu_button "$node" request send
		fi

		request_deadline=$((SECONDS + 30))
		while ((SECONDS <= request_deadline)); do
			menu_snapshot "$node" "$http_client_menu"
			status="$(menu_status "$http_client_menu")"
			err="$(menu_field "$http_client_menu" error)"
			response="$(menu_field "$http_client_menu" response)"
			if [[ "$status" == "Status: 200 OK" && -z "$err" && "$response" == "Debug http server response" ]]; then
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				break
			fi
			sleep 0.25
		done

		sleep 0.5
	done

	fail "HTTP client did not receive nested WireGuard response; status=$status error=$err response=$response"
}

assert_udp_ping_works() {
	local node="$1"
	local deadline=$((SECONDS + 90))
	local request_deadline
	local status=""
	local err=""
	local response=""

	while ((SECONDS <= deadline)); do
		menu_snapshot "$node" "$udp_client_menu"
		status="$(menu_status "$udp_client_menu")"
		if [[ "$status" != "Status: running..." ]]; then
			click_menu_button "$node" config send
		fi

		request_deadline=$((SECONDS + 15))
		while ((SECONDS <= request_deadline)); do
			menu_snapshot "$node" "$udp_client_menu"
			status="$(menu_status "$udp_client_menu")"
			err="$(menu_field "$udp_client_menu" error)"
			response="$(menu_field "$udp_client_menu" response)"
			if [[ "$status" == "Status: ok" && -z "$err" && "$response" == "pong:nested-wireguard-udp-ping" ]]; then
				return 0
			fi
			if [[ "$status" != "Status: running..." ]]; then
				break
			fi
			sleep 0.25
		done

		sleep 0.5
	done

	fail "UDP ping did not receive nested WireGuard response; status=$status error=$err response=$response"
}

start_daemon

wait_node_class_available "almagest/UDPPingClient" \
	|| fail "debug UDP ping client node class is unavailable"

wait_graph_node "MockNetwork 1500" || fail "MockNetwork node missing"
wait_graph_node "WireGuard Client 1501" || fail "outer client WireGuard node missing"
wait_graph_node "WireGuard Server 1502" || fail "outer server WireGuard node missing"
wait_graph_node "VTun Client 1503" || fail "outer client VTun node missing"
wait_graph_node "VTun Server 1504" || fail "outer server VTun node missing"
wait_graph_node "Inner WireGuard Client 1507" || fail "inner client WireGuard node missing"
wait_graph_node "Inner WireGuard Server 1508" || fail "inner server WireGuard node missing"
wait_graph_node "Inner VTun Client 1509" || fail "inner client VTun node missing"
wait_graph_node "Inner VTun Server 1510" || fail "inner server VTun node missing"
wait_graph_node "UDPPingServer 1505" || fail "UDP ping server node missing"
wait_graph_node "UDPPingClient 1506" || fail "UDP ping client node missing"
wait_graph_node "HttpServer 1607" || fail "HTTP server node missing"
wait_graph_node "HttpClient 1612" || fail "HTTP client node missing"

outer_client_wg="$(node_id_by_name "WireGuard Client 1501")"
outer_server_wg="$(node_id_by_name "WireGuard Server 1502")"
outer_client_vtun="$(node_id_by_name "VTun Client 1503")"
outer_server_vtun="$(node_id_by_name "VTun Server 1504")"
inner_client_wg="$(node_id_by_name "Inner WireGuard Client 1507")"
inner_server_wg="$(node_id_by_name "Inner WireGuard Server 1508")"
inner_client_vtun="$(node_id_by_name "Inner VTun Client 1509")"
inner_server_vtun="$(node_id_by_name "Inner VTun Server 1510")"
udp_client="$(node_id_by_name "UDPPingClient 1506")"
http_client="$(node_id_by_name "HttpClient 1612")"

wait_log_contains "almagest/HttpServer" "listening on 10.199.20.1:8080" \
	|| fail "HTTP server did not start listening"
wait_log_contains "almagest/UDPPingServer" "udp server listening on 10.199.20.1:26120" \
	|| fail "UDP ping server did not start listening"

wait_node_label "$outer_client_wg" up || fail "outer client WireGuard node did not become up"
wait_node_label "$outer_server_wg" up || fail "outer server WireGuard node did not become up"
wait_node_label "$inner_client_wg" up || fail "inner client WireGuard node did not become up"
wait_node_label "$inner_server_wg" up || fail "inner server WireGuard node did not become up"
wait_node_label "$outer_client_vtun" wireguard-client-vtun || fail "outer client VTun did not become ready"
wait_node_label "$outer_server_vtun" wireguard-server-vtun || fail "outer server VTun did not become ready"
wait_node_label "$inner_client_vtun" inner-wireguard-client-vtun || fail "inner client VTun did not become ready"
wait_node_label "$inner_server_vtun" inner-wireguard-server-vtun || fail "inner server VTun did not become ready"

assert_udp_ping_works "$udp_client"
wait_node_label "$udp_client" ok || fail "UDP ping client node did not report ok"

assert_http_request_works "$http_client"
wait_node_label "$http_client" "200 OK" || fail "HTTP client node did not report 200 OK"

wait_log_contains "almagest/UDPPingClient" "udp ping" \
	|| fail "UDP ping client did not log the ping"
wait_log_contains "almagest/UDPPingServer" "udp pong" \
	|| fail "UDP ping server did not log the pong"
wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the nested WireGuard request"

log "case passed"
