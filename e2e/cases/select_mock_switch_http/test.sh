#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

assert_socks_body() {
	local socks_port="$1"
	local expected="$2"
	local out="/tmp/e2e-select-switch-${socks_port}.txt"

	retry "$ALMAGEST_E2E_TIMEOUT" "SOCKS $socks_port request returns $expected" \
		curl -g --fail --silent --show-error \
			--socks5-hostname "127.0.0.1:$socks_port" \
			http://127.0.0.1:8080 \
			--output "$out" \
		|| fail "SOCKS $socks_port request to mock HTTP server did not succeed"

	local body
	body="$(cat "$out")"
	[[ "$body" == "$expected" ]] \
		|| fail "unexpected response through SOCKS $socks_port: $body"
}

set_bool_constant() {
	local node="$1"
	local value="$2"

	cli engine send-node-formular-msg \
		--node "$node" \
		--message "{\"type\":\"field.update\",\"menuId\":\"NODE${node}MENU\",\"field\":{\"blockId\":\"state\",\"fieldId\":\"value\"},\"value\":$value}" \
		>/tmp/e2e-select-switch-bool-update.json
}

start_daemon

wait_graph_node "BoolConstant 1815" || fail "BoolConstant node missing"
wait_graph_node "LocalProxy 1857" || fail "first LocalProxy node missing"
wait_graph_node "LocalProxy 1859" || fail "second LocalProxy node missing"
wait_graph_node "Select 1810" || fail "first Select node missing"
wait_graph_node "Select 1831" || fail "second Select node missing"
wait_graph_node "SelectOut 1826" || fail "first SelectOut node missing"
wait_graph_node "SelectOut 1836" || fail "second SelectOut node missing"
wait_graph_node "MockNetwork 1853" || fail "first MockNetwork node missing"
wait_graph_node "MockNetwork 1855" || fail "second MockNetwork node missing"
wait_graph_node "HttpServer 1867" || fail "Server A HTTP node missing"
wait_graph_node "HttpServer 1873" || fail "Server B HTTP node missing"

wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8080" \
	|| fail "HTTP servers did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "first local proxy did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1082" \
	|| fail "second local proxy did not start listening"

assert_socks_body 1081 "Server A"
assert_socks_body 1082 "Server B"

bool_node="$(node_id_by_name "BoolConstant 1815")"
set_bool_constant "$bool_node" true
wait_node_label "$bool_node" "true"

assert_socks_body 1081 "Server B"
assert_socks_body 1082 "Server A"

log "case passed"
