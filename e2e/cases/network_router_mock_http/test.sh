#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

assert_proxied_body() {
	local host="$1"
	local expected="$2"
	local out="/tmp/e2e-network-router-${host}.txt"

	retry "$ALMAGEST_E2E_TIMEOUT" "proxied HTTP request to $host" \
		curl -g --fail --silent --show-error \
			--socks5-hostname 127.0.0.1:1081 \
			"http://$host/" \
			--output "$out" \
		|| fail "proxied request to $host did not succeed"

	local body
	body="$(cat "$out")"
	[[ "$body" == "$expected" ]] \
		|| fail "unexpected response body for $host: $body"
}

start_daemon

wait_graph_node "LocalProxy 1684" || fail "LocalProxy node missing"
wait_graph_node "NetworkRouter 1686" || fail "NetworkRouter node missing"
wait_graph_node "MockNetwork 1692" || fail "slot 1 MockNetwork node missing"
wait_graph_node "MockNetwork 1694" || fail "slot 2 MockNetwork node missing"
wait_graph_node "MockNetwork 1696" || fail "slot 3 MockNetwork node missing"
wait_graph_node "HttpServer 1701" || fail "Server 1 HTTP node missing"
wait_graph_node "HttpServer 1710" || fail "Server 2 HTTP node missing"
wait_graph_node "HttpServer 1719" || fail "Server 3 HTTP node missing"

wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

assert_proxied_body "host1" "Server 1"
assert_proxied_body "host2" "Server 2"
assert_proxied_body "not-host1-or-host2" "Server 3"

wait_log_contains "almagest/LocalProxy" "connect host1:80" \
	|| fail "local proxy did not log the host1 connect request"
wait_log_contains "almagest/LocalProxy" "connect host2:80" \
	|| fail "local proxy did not log the host2 connect request"
wait_log_contains "almagest/LocalProxy" "connect not-host1-or-host2:80" \
	|| fail "local proxy did not log the default route connect request"

log "case passed"
