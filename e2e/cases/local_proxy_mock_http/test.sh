#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

proxied_body_is() {
	local port="$1"
	local expected="$2"
	local output="/tmp/e2e-local-proxy-${port}.body"

	curl --fail --silent --show-error --max-time 2 \
		--socks5-hostname 127.0.0.1:1081 \
		"http://127.0.0.1:${port}/" \
		--output "$output" \
		2>/dev/null \
		&& [[ "$(<"$output")" == "$expected" ]]
}

assert_proxied_body() {
	local port="$1"
	local expected="$2"
	local description="$3"

	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		proxied_body_is "$port" "$expected" \
		|| fail "$description did not return $expected"
}

proxied_request_fails() {
	local port="$1"
	! curl --fail --silent --show-error --max-time 1 \
		--socks5-hostname 127.0.0.1:1081 \
		"http://127.0.0.1:${port}/" \
		--output /dev/null \
		2>/dev/null
}

assert_proxied_failure() {
	local port="$1"
	local description="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		proxied_request_fails "$port" \
		|| fail "$description stayed reachable"
}

set_response() {
	local node="$1"
	local value="$2"
	jq -n --arg value "$value" '{"state":{"value":$value}}' \
		| cli graph apply-menu --node "$node" >/dev/null
}

set_server_port() {
	local port="$1"
	jq -n --argjson port "$port" '{"listen":{"port":$port}}' \
		| cli graph apply-menu --name "HttpServer 1001" >/dev/null
}

start_daemon

wait_node_class_available "almagest/LogNetwork" \
	|| fail "debug network log node class is unavailable"

wait_graph_node "HttpServer 1001" || fail "HttpServer node missing"
wait_graph_node "LocalProxy 1002" || fail "LocalProxy node missing"
wait_graph_node "MockNetwork 1003" || fail "MockNetwork node missing"
wait_graph_node "LogNetwork 1004" || fail "server LogNetwork node missing"
wait_graph_node "LogNetwork 1005" || fail "proxy LogNetwork node missing"
wait_graph_node "HTTP Response" || fail "HTTP response constant node missing"

wait_log_contains "almagest/HttpServer" "listening on 127.0.0.1:8091" \
	|| fail "HTTP server did not start listening"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

assert_proxied_body 8091 "Debug http server response" \
	"initial proxied HTTP request"

wait_log_contains "almagest/LocalProxy" "connect 127.0.0.1:8091" \
	|| fail "local proxy did not log the connect request"
wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP server did not log the GET request"
wait_log_contains "almagest/LogNetwork" "Dial tcp" \
	|| fail "debug network log did not record the dial"
wait_log_contains "almagest/LogNetwork" "GET / HTTP/1.1" \
	|| fail "debug network log did not record request bytes"
wait_log_contains "almagest/LogNetwork" "Debug http server response" \
	|| fail "debug network log did not record response bytes"

assert_proxied_failure 8092 "request to the initial unused port"

assert_log_not_contains "almagest/HttpServer" "8092"
assert_daemon_log_contains "engine built in"

server="$(node_id_by_name "HttpServer 1001")"
response="$(node_id_by_name "HTTP Response")"
server_log="$(node_id_by_name "LogNetwork 1004")"
network="$(node_id_by_name "MockNetwork 1003")"

# Queue several config changes while the old server worker can still be
# stopping. Only the newest desired port and response may remain active.
set_response "$response" "stale response one"
set_server_port 8092
set_response "$response" "stale response two"
set_server_port 8093
set_response "$response" "latest burst response"
assert_proxied_body 8093 "latest burst response" \
	"request after rapid server reconfiguration"
assert_proxied_failure 8091 "request to the original server port"
assert_proxied_failure 8092 "request to the intermediate server port"

# Detach the server while its runtime is live, update the desired state while
# no network is attached, then restore the same edge. The first request must
# use the current port and response.
server_link="$(link_id_by_port_names "HttpServer 1001" Network "LogNetwork 1004" Network)"
cli graph delete-link --id "$server_link" >/dev/null
assert_proxied_failure 8093 "request after server network detach"
set_response "$response" "detached latest response"
set_server_port 8094
add_link_by_port_name "$server" right Network "$server_log" left Network >/dev/null
assert_proxied_body 8094 "detached latest response" \
	"first request after server network reattach"

# Replacing the logger's upstream invalidates the network wrapper held by the
# server. A replacement wrapper must restart the listener with current state.
upstream_link="$(link_id_by_port_names "LogNetwork 1004" Network "MockNetwork 1003" Network)"
cli graph delete-link --id "$upstream_link" >/dev/null
assert_proxied_failure 8094 "request after logger upstream detach"
set_response "$response" "replacement wrapper response"
add_link_by_port_name "$server_log" right Network "$network" left Network >/dev/null
assert_proxied_body 8094 "replacement wrapper response" \
	"first request after logger upstream replacement"

# Burst response changes exercise repeated worker stop notifications. A late
# completion from an older worker must not restore an earlier response.
for value in generation-1 generation-2 generation-3 generation-4 generation-5 final-generation; do
	set_response "$response" "$value"
done
assert_proxied_body 8094 final-generation \
	"request after repeated response changes"

# Node-owned values and graph edits must restore into working runtime objects.
stop_daemon
start_daemon
assert_proxied_body 8094 final-generation \
	"first request after daemon restart"

log "case passed"
