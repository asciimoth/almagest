#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

local_ns="alm-e2e-local"
public_ns="alm-e2e-public"
local_target="10.213.1.2:18093"
public_target="203.0.113.2:18094"
local_body="DefaultTun local safe route response"
public_body="DefaultTun public routed response"
local_log="/tmp/e2e-default-tun-local.log"
public_log="/tmp/e2e-default-tun-public.log"
local_pid=""
public_pid=""

cleanup() {
	stop_static_http_target "$local_pid"
	stop_static_http_target "$public_pid"
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
}

curl_target() {
	local target="$1"
	local path="$2"
	local output="$3"
	curl --fail --silent --show-error --connect-timeout 1 --max-time 3 \
		"http://$target$path" \
		--output "$output"
}

curl_target_fails() {
	local target="$1"
	local path="$2"
	if curl_target "$target" "$path" /tmp/e2e-default-tun-unexpected-body.txt \
		>/tmp/e2e-default-tun-unexpected-stdout.txt \
		2>/tmp/e2e-default-tun-expected-failure.txt; then
		return 1
	fi
	return 0
}

assert_body() {
	local file="$1"
	local want="$2"
	local got
	got="$(cat "$file")"
	[[ "$got" == "$want" ]] || fail "unexpected response body: $got"
}

start_servers() {
	local_pid="$(start_static_http_target_in_netns "$local_ns" 10.213.1.2 18093 "$local_body" "$local_log")"
	public_pid="$(start_static_http_target_in_netns "$public_ns" 203.0.113.2 18094 "$public_body" "$public_log")"
}

start_servers
trap cleanup EXIT

retry "$ALMAGEST_E2E_TIMEOUT" "baseline local target" \
	curl_target "$local_target" /baseline-local /tmp/e2e-default-tun-local-baseline.txt \
	|| fail "local target was not reachable before daemon startup"
assert_body /tmp/e2e-default-tun-local-baseline.txt "$local_body"

retry "$ALMAGEST_E2E_TIMEOUT" "baseline public target" \
	curl_target "$public_target" /baseline-public /tmp/e2e-default-tun-public-baseline.txt \
	|| fail "public routed target was not reachable before daemon startup"
assert_body /tmp/e2e-default-tun-public-baseline.txt "$public_body"

start_daemon

wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_node_class_available "almagest/Spoofer" \
	|| fail "Spoofer node class is unavailable"
wait_node_class_available "almagest/OutNetwork" \
	|| fail "OutNetwork node class is unavailable"
wait_node_class_available "debug.almagest/LogNetwork" \
	|| fail "debug network log node class is unavailable"
wait_graph_node "DefaultTun 1301" || fail "DefaultTun node missing"
default_tun="$(node_id_by_name "DefaultTun 1301")"
wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"

retry "$ALMAGEST_E2E_TIMEOUT" "local route with non-strict DefaultTun" \
	curl_target "$local_target" /non-strict-local /tmp/e2e-default-tun-local-nonstrict.txt \
	|| fail "local directly connected target should bypass non-strict DefaultTun"
assert_body /tmp/e2e-default-tun-local-nonstrict.txt "$local_body"

retry "$ALMAGEST_E2E_TIMEOUT" "public route blocked by disconnected DefaultTun" \
	curl_target_fails "$public_target" /blocked-by-default-tun \
	|| fail "public routed target unexpectedly bypassed disconnected DefaultTun"

cli graph apply-menu --node "$default_tun" --json '{"config":{"strict":true}}' >/tmp/e2e-default-tun-strict.json

retry "$ALMAGEST_E2E_TIMEOUT" "local route blocked by strict DefaultTun" \
	curl_target_fails "$local_target" /blocked-by-strict \
	|| fail "local target unexpectedly bypassed strict DefaultTun"
retry "$ALMAGEST_E2E_TIMEOUT" "public route still blocked by strict DefaultTun" \
	curl_target_fails "$public_target" /blocked-public-strict \
	|| fail "public target unexpectedly bypassed strict DefaultTun"

cli graph apply-menu --node "$default_tun" --json '{"config":{"strict":false}}' >/tmp/e2e-default-tun-nonstrict.json

retry "$ALMAGEST_E2E_TIMEOUT" "local route restored after disabling strict mode" \
	curl_target "$local_target" /strict-disabled-local /tmp/e2e-default-tun-local-restored.txt \
	|| fail "local target was not restored after disabling strict mode"
assert_body /tmp/e2e-default-tun-local-restored.txt "$local_body"

retry "$ALMAGEST_E2E_TIMEOUT" "public route remains blocked before OutNetwork chain" \
	curl_target_fails "$public_target" /still-blocked-before-chain \
	|| fail "public target unexpectedly worked before OutNetwork chain was attached"

spoofer="$(add_node "almagest/Spoofer" "Spoofer 1302")"
log_network="$(add_node "debug.almagest/LogNetwork" "LogNetwork 1303")"
out_network="$(add_node "almagest/OutNetwork" "OutNetwork 1304")"

add_link_by_port_name "$default_tun" right Tun "$spoofer" left Tun >/tmp/e2e-default-tun-link-tun.json
add_link_by_port_name "$spoofer" right Network "$log_network" left Network >/tmp/e2e-default-tun-link-log.json
add_link_by_port_name "$log_network" right Network "$out_network" left Network >/tmp/e2e-default-tun-link-out.json

wait_node_label "$spoofer" forwarding || fail "Spoofer did not start forwarding"

retry "$ALMAGEST_E2E_TIMEOUT" "public route through DefaultTun Spoofer OutNetwork chain" \
	curl_target "$public_target" /through-outnetwork-chain /tmp/e2e-default-tun-public-chain.txt \
	|| fail "public target did not work through DefaultTun/Spoofer/OutNetwork chain"
assert_body /tmp/e2e-default-tun-public-chain.txt "$public_body"

wait_log_contains "debug.almagest/LogNetwork" "Dial tcp $public_target" \
	|| fail "debug network log did not record public target dial"
wait_log_contains "debug.almagest/LogNetwork" "GET /through-outnetwork-chain" \
	|| fail "debug network log did not record proxied public request"
wait_log_contains "debug.almagest/LogNetwork" "$public_body" \
	|| fail "debug network log did not record proxied public response"

stop_daemon

retry "$ALMAGEST_E2E_TIMEOUT" "public route restored after daemon stop" \
	curl_target "$public_target" /after-daemon-stop /tmp/e2e-default-tun-public-stopped.txt \
	|| fail "public target was not restored after daemon shutdown"
assert_body /tmp/e2e-default-tun-public-stopped.txt "$public_body"

grep -F "GET /baseline-local" "$local_log" >/dev/null \
	|| fail "local target did not receive baseline request"
grep -F "GET /after-daemon-stop" "$public_log" >/dev/null \
	|| fail "public target did not receive post-daemon request"

log "case passed"
