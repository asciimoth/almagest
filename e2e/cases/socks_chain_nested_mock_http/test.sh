#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

curl_err="/tmp/e2e-socks-chain-nested-curl.err"
curl_out="/tmp/e2e-socks-chain-nested-body.txt"

nested_socks_request() {
	curl -g --fail --silent --show-error \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:8080 \
		--output "$curl_out" \
		2>"$curl_err"
}

start_daemon

wait_graph_node "LocalProxy 2013" || fail "public LocalProxy node missing"
wait_graph_node "SocksClient 2096" || fail "nested SocksClient node missing"
wait_graph_node "SocksClient 2015" || fail "chain SocksClient node missing"
wait_graph_node "LocalProxy 2020" || fail "chain LocalProxy node missing"
wait_graph_node "SocksClient 2022" || fail "second chain SocksClient node missing"
wait_graph_node "LocalProxy 2027" || fail "second chain LocalProxy node missing"
wait_graph_node "LocalProxy 2094" || fail "mock LocalProxy node missing"
wait_graph_node "MockNetwork 2035" || fail "MockNetwork node missing"
wait_graph_node "HttpServer 2043" || fail "HttpServer node missing"

wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "public local proxy did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1082" \
	|| fail "chain local proxy did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1083" \
	|| fail "second chain local proxy did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1084" \
	|| fail "mock local proxy did not start listening"
wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8080" \
	|| fail "mock HTTP server did not start listening"

if ! retry "$ALMAGEST_E2E_TIMEOUT" "nested SOCKS request to mock HTTP server" nested_socks_request; then
	if [[ -s "$curl_err" ]]; then
		sed 's/^/[curl] /' "$curl_err" >&2
	fi
	fail "nested SOCKS request to mock HTTP server did not succeed"
fi

body="$(cat "$curl_out")"
[[ "$body" == "Debug http server response" ]] \
	|| fail "unexpected nested SOCKS response body: $body"

wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:8080" \
	|| fail "local proxy did not log the final HTTP connect request"
wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:1084" \
	|| fail "local proxy did not log the nested SOCKS connect request"
wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the proxied request"

log "case passed"
