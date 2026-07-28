#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="Mock isolation outside target response"
target_log="/tmp/e2e-mock-isolation-target.log"
target_pid=""

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
}

start_daemon
trap cleanup EXIT

target_pid="$(start_static_http_target 127.0.0.1 18092 "$target_body" "$target_log")"

wait_node_class_available "almagest/LogNetwork" \
	|| fail "debug network log node class is unavailable"

wait_graph_node "HttpServer 1201" || fail "HttpServer node missing"
wait_graph_node "LocalProxy 1202" || fail "LocalProxy node missing"
wait_graph_node "MockNetwork 1203" || fail "MockNetwork node missing"
wait_graph_node "LogNetwork 1204" || fail "server LogNetwork node missing"
wait_graph_node "LogNetwork 1205" || fail "proxy LogNetwork node missing"

wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8091" \
	|| fail "HTTP server did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

retry "$ALMAGEST_E2E_TIMEOUT" "direct HTTP request outside MockNetwork" \
	curl --fail --silent --show-error \
		http://127.0.0.1:18092/direct-outside-mock \
		--output /tmp/e2e-mock-isolation-direct-body.txt \
	|| fail "direct request outside MockNetwork did not succeed"

direct_body="$(cat /tmp/e2e-mock-isolation-direct-body.txt)"
[[ "$direct_body" == "$target_body" ]] \
	|| fail "unexpected direct response body: $direct_body"

assert_log_not_contains "almagest/LogNetwork" "direct-outside-mock"
assert_log_not_contains "almagest/HttpServer" "direct-outside-mock"

retry "$ALMAGEST_E2E_TIMEOUT" "proxied HTTP request inside MockNetwork" \
	curl --fail --silent --show-error \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:8091/ \
		--output /tmp/e2e-mock-isolation-proxied-body.txt \
	|| fail "proxied request inside MockNetwork did not succeed"

proxied_body="$(cat /tmp/e2e-mock-isolation-proxied-body.txt)"
[[ "$proxied_body" == "Debug http server response" ]] \
	|| fail "unexpected proxied MockNetwork response body: $proxied_body"

wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:8091" \
	|| fail "local proxy did not log the MockNetwork connect request"
wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the MockNetwork request"
wait_log_contains "almagest/LogNetwork" "Debug http server response" \
	|| fail "debug network log did not record MockNetwork response bytes"

if curl --fail --silent --show-error --max-time 2 \
	--socks5-hostname 127.0.0.1:1081 \
	http://127.0.0.1:18092/blocked-through-mock \
	>/tmp/e2e-mock-isolation-unexpected-body.txt \
	2>/tmp/e2e-mock-isolation-expected-failure.txt; then
	fail "proxied request to OS target unexpectedly escaped MockNetwork"
fi

grep -F "GET /direct-outside-mock" "$target_log" >/dev/null \
	|| fail "OS target did not receive the direct request"
if grep -F "GET /blocked-through-mock" "$target_log" >/dev/null; then
	fail "OS target received a request that should have been isolated by MockNetwork"
fi

log "case passed"

