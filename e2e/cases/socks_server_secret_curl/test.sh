#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="curl to Almagest server secret"
target_log="$E2E_RUNTIME/server-secret-target.log"
target_pid=""

cleanup() {
	stop_static_http_target "$target_pid"
	stop_daemon
}

target_pid="$(start_static_http_target 127.0.0.1 18097 "$target_body" "$target_log")"
start_daemon
trap cleanup EXIT

wait_graph_node "SOCKS Server Secret" || fail "SOCKS server node missing"
wait_log_contains "almagest/SocksServer" "socks listening on 127.0.0.1:11097" \
	|| fail "SOCKS server did not start listening"

if curl --fail --silent --show-error \
	--socks5-hostname 127.0.0.1:11097 \
	--proxy-user alice:wrong-inline-password \
	http://127.0.0.1:18097/inline-password \
	--output "$E2E_RUNTIME/inline-password-body.txt" 2>/dev/null; then
	fail "SOCKS server accepted the overwritten inline password"
fi

retry "$ALMAGEST_E2E_TIMEOUT" "curl request with the secret-backed password" \
	curl --fail --silent --show-error \
		--socks5-hostname 127.0.0.1:11097 \
		--proxy-user alice:curl-socks-secret \
		http://127.0.0.1:18097/server-secret \
		--output "$E2E_RUNTIME/server-secret-body.txt" \
	|| fail "curl did not connect through the secret-backed SOCKS server"

body="$(cat "$E2E_RUNTIME/server-secret-body.txt")"
[[ "$body" == "$target_body" ]] || fail "unexpected target response: $body"
grep -F "GET /server-secret" "$target_log" >/dev/null \
	|| fail "external HTTP target did not receive the curl request"

log "case passed"
