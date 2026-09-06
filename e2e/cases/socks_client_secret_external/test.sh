#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="Almagest client secret to MicroSocks"
target_log="$E2E_RUNTIME/client-secret-target.log"
target_pid=""
microsocks_pid=""

cleanup() {
	stop_static_http_target "$target_pid"
	if [[ -n "$microsocks_pid" ]]; then
		kill "$microsocks_pid" 2>/dev/null || true
		wait "$microsocks_pid" 2>/dev/null || true
	fi
	stop_daemon
}

microsocks -i 127.0.0.1 -p 11096 -u alice -P micro-socks-secret \
	>"$E2E_RUNTIME/microsocks.log" 2>&1 &
microsocks_pid="$!"
target_pid="$(start_static_http_target 127.0.0.1 18096 "$target_body" "$target_log")"

start_daemon
trap cleanup EXIT

wait_graph_node "SOCKS Client Secret" || fail "SOCKS client node missing"
wait_graph_node "HTTP Client External SOCKS" || fail "HTTP client node missing"

http_client="$(node_id_by_name "HTTP Client External SOCKS")"

request_succeeded() {
	local label
	label="$(node_label "$http_client")" || return 1
	if [[ "$label" == "200 OK" ]]; then
		return 0
	fi
	if [[ "$label" != "running..." ]]; then
		cli graph click-menu-button --node "$http_client" --block request --button send >/dev/null || return 1
	fi
	return 1
}

retry "$ALMAGEST_E2E_TIMEOUT" "HTTP client request through MicroSocks" request_succeeded \
	|| fail "HTTP client did not connect through the external SOCKS server"

menu="$E2E_RUNTIME/http-client-menu.json"
cli graph menu --node "$http_client" >"$menu"
jq -e --arg body "$target_body" '
	.blocks[]
	| select(.id == "result")
	| .items[]
	| select(.id == "response" and .value == $body)
' "$menu" >/dev/null || fail "HTTP client returned an unexpected response"

grep -F "GET /client-secret" "$target_log" >/dev/null \
	|| fail "external HTTP target did not receive the SOCKS request"

log "case passed"
