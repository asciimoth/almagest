#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

start_daemon

wait_node_class_available "debug.almagest/LogNetwork" \
	|| fail "debug network log node class is unavailable"

wait_graph_node "HttpServer 1001" || fail "HttpServer node missing"
wait_graph_node "LocalProxy 1002" || fail "LocalProxy node missing"
wait_graph_node "MockNetwork 1003" || fail "MockNetwork node missing"
wait_graph_node "LogNetwork 1004" || fail "server LogNetwork node missing"
wait_graph_node "LogNetwork 1005" || fail "proxy LogNetwork node missing"

wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8091" \
	|| fail "HTTP server did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

retry "$ALMAGEST_E2E_TIMEOUT" "proxied HTTP request to 127.0.0.1:8091" \
	curl --fail --silent --show-error \
		--socks5-hostname 127.0.0.1:1081 \
		http://127.0.0.1:8091/ \
		--output /tmp/e2e-local-proxy-body.txt \
	|| fail "proxied request to HTTP server did not succeed"

body="$(cat /tmp/e2e-local-proxy-body.txt)"
[[ "$body" == "Debug http server response" ]] \
	|| fail "unexpected proxied response body: $body"

wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:8091" \
	|| fail "local proxy did not log the connect request"
wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the GET request"
wait_log_contains "debug.almagest/LogNetwork" "Dial tcp" \
	|| fail "debug network log did not record the dial"
wait_log_contains "debug.almagest/LogNetwork" "GET / HTTP/1.1" \
	|| fail "debug network log did not record request bytes"
wait_log_contains "debug.almagest/LogNetwork" "Debug http server response" \
	|| fail "debug network log did not record response bytes"

if curl --fail --silent --show-error --max-time 2 \
	--socks5-hostname 127.0.0.1:1081 \
	http://127.0.0.1:8092/ >/tmp/e2e-unexpected-response.txt 2>/tmp/e2e-expected-curl-failure.txt; then
	fail "proxied request to unused local port unexpectedly succeeded"
fi

assert_log_not_contains "almagest/HttpServer" "8092"
assert_daemon_log_contains "engine built in"

log "case passed"
