#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="OutNetwork e2e target response"
target_log="/tmp/e2e-out-http-target.log"
target_pid=""

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
}

start_daemon
trap cleanup EXIT

target_pid="$(start_static_http_target 127.0.0.1 18091 "$target_body" "$target_log")"

wait_node_class_available "debug.almagest/LogNetwork" \
	|| fail "debug network log node class is unavailable"

wait_graph_node "LocalProxy 1101" || fail "LocalProxy node missing"
wait_graph_node "LogNetwork 1102" || fail "LogNetwork node missing"
wait_graph_node "OutNetwork 1103" || fail "OutNetwork node missing"

wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

retry "$ALMAGEST_E2E_TIMEOUT" "direct HTTP request to OS target" \
	curl --fail --silent --show-error \
		http://127.0.0.1:18091/direct-outside-proxy \
		--output /tmp/e2e-out-http-direct-body.txt \
	|| fail "direct request to OS target did not succeed"

direct_body="$(cat /tmp/e2e-out-http-direct-body.txt)"
[[ "$direct_body" == "$target_body" ]] \
	|| fail "unexpected direct response body: $direct_body"

assert_log_not_contains "debug.almagest/LogNetwork" "direct-outside-proxy"
assert_log_not_contains "almagest/LocalProxy" "18091"

retry "$ALMAGEST_E2E_TIMEOUT" "proxied HTTP request through OutNetwork" \
	curl --fail --silent --show-error \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:18091/proxied-outnetwork \
		--output /tmp/e2e-out-http-proxied-body.txt \
	|| fail "proxied request through OutNetwork did not succeed"

proxied_body="$(cat /tmp/e2e-out-http-proxied-body.txt)"
[[ "$proxied_body" == "$target_body" ]] \
	|| fail "unexpected proxied response body: $proxied_body"

grep -F "GET /direct-outside-proxy" "$target_log" >/dev/null \
	|| fail "target did not receive the direct request"
grep -F "GET /proxied-outnetwork" "$target_log" >/dev/null \
	|| fail "target did not receive the proxied request"

wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:18091" \
	|| fail "local proxy did not log the OutNetwork connect request"
wait_log_contains "debug.almagest/LogNetwork" "Dial tcp4 127.0.0.1:18091" \
	|| fail "debug network log did not record the OutNetwork dial"
wait_log_contains "debug.almagest/LogNetwork" "GET /proxied-outnetwork" \
	|| fail "debug network log did not record proxied request bytes"
wait_log_contains "debug.almagest/LogNetwork" "$target_body" \
	|| fail "debug network log did not record proxied response bytes"

log "case passed"
