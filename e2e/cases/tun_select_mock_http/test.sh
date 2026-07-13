#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

cleanup() {
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-tun.sh" ]]; then
		"$E2E_RUNTIME/cleanup-tun.sh"
	fi
}

curl_url() {
	local url="$1"
	local output="$2"
	curl --fail --silent --show-error --noproxy '*' \
		--connect-timeout 2 --max-time 5 \
		"$url" \
		--output "$output"
}

assert_tun_body() {
	local url="$1"
	local expected="$2"
	local output="$3"

	retry "$ALMAGEST_E2E_TIMEOUT" "$url returns $expected" \
		curl_url "$url" "$output" \
		|| fail "request to $url did not succeed"

	local body
	body="$(cat "$output")"
	[[ "$body" == "$expected" ]] \
		|| fail "unexpected response from $url: $body"
}

set_bool_constant() {
	local node="$1"
	local value="$2"

	cli graph apply-menu \
		--node "$node" \
		--json "{\"state\":{\"value\":$value}}" \
		>/tmp/e2e-tun-select-bool-apply.json
}

trap cleanup EXIT

start_daemon

wait_node_class_available "almagest/Tun" \
	|| fail "Tun node class is unavailable"
wait_node_class_available "almagest/Spoofer" \
	|| fail "Spoofer node class is unavailable"
wait_node_class_available "almagest/MockNetwork" \
	|| fail "MockNetwork node class is unavailable"
wait_node_class_available "almagest/HttpServer" \
	|| fail "HttpServer node class is unavailable"

wait_graph_node "BoolConstant 1815" || fail "BoolConstant node missing"
wait_graph_node "Tun 1901" || fail "first Tun node missing"
wait_graph_node "Tun 1902" || fail "second Tun node missing"
wait_graph_node "SelectOut 1826" || fail "first SelectOut node missing"
wait_graph_node "SelectOut 1836" || fail "second SelectOut node missing"
wait_graph_node "Select 1810" || fail "first Select node missing"
wait_graph_node "Select 1831" || fail "second Select node missing"
wait_graph_node "Spoofer 1910" || fail "Server A Spoofer node missing"
wait_graph_node "Spoofer 1911" || fail "Server B Spoofer node missing"
wait_graph_node "MockNetwork 1853" || fail "Server A MockNetwork node missing"
wait_graph_node "MockNetwork 1855" || fail "Server B MockNetwork node missing"

bool_node="$(node_id_by_name "BoolConstant 1815")"
tun_a="$(node_id_by_name "Tun 1901")"
tun_b="$(node_id_by_name "Tun 1902")"
spoofer_a="$(node_id_by_name "Spoofer 1910")"
spoofer_b="$(node_id_by_name "Spoofer 1911")"

wait_node_label "$tun_a" tun-select-a || fail "first Tun did not become ready"
wait_node_label "$tun_b" tun-select-b || fail "second Tun did not become ready"
wait_node_label "$spoofer_a" forwarding || fail "Server A Spoofer did not start forwarding"
wait_node_label "$spoofer_b" forwarding || fail "Server B Spoofer did not start forwarding"

wait_log_contains "almagest/HttpServer" "listening on 10.71.160.2:8080" \
	|| fail "target A HTTP bindings did not start listening"
wait_log_contains "almagest/HttpServer" "listening on 10.72.160.2:8080" \
	|| fail "target B HTTP bindings did not start listening"

assert_tun_body http://10.71.160.2:8080/ "Server A" /tmp/e2e-tun-select-initial-a.txt
assert_tun_body http://10.72.160.2:8080/ "Server B" /tmp/e2e-tun-select-initial-b.txt

set_bool_constant "$bool_node" true
wait_node_label "$bool_node" "true"

assert_tun_body http://10.71.160.2:8080/ "Server B" /tmp/e2e-tun-select-swapped-a.txt
assert_tun_body http://10.72.160.2:8080/ "Server A" /tmp/e2e-tun-select-swapped-b.txt

wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP servers did not log GET requests"

log "case passed"
