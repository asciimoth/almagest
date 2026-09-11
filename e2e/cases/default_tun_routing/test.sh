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

default_tun_name() {
	ip -j link show | jq -er \
		'.[] | select(.ifname | startswith("alm-")) | .ifname' \
		| head -1
}

link_is_absent() {
	local name="$1"
	! ip link show "$name" >/dev/null 2>&1
}

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

default_tun_mtu_is() {
	local name="$1"
	local mtu="$2"
	[[ "$(ip -j link show "$name" | jq -er '.[0].mtu')" == "$mtu" ]]
}

set_default_tun_mtu() {
	local node="$1"
	local mtu="$2"
	cli graph apply-menu --node "$node" --json \
		"{\"config\":{\"mtu\":$mtu}}" \
		>"$E2E_RUNTIME/default-tun-mtu-$mtu.json"
}

set_public_carrier_mtu() {
	local mtu="$1"
	ip link set almr0 mtu "$mtu"
	ip -n alm-e2e-router link set almr1 mtu "$mtu"
	ip -n alm-e2e-router link set almp0 mtu "$mtu"
	ip -n alm-e2e-public link set almp1 mtu "$mtu"
}

curl_target_large() {
	local target="$1"
	local path="$2"
	local output="$3"
	local header
	printf -v header '%*s' 8192 ''
	header="${header// /x}"
	curl --fail --silent --show-error --connect-timeout 2 --max-time 8 \
		-H "X-GSO-Probe: $header" \
		"http://$target$path" \
		--output "$output"
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
wait_node_class_available "almagest/LogNetwork" \
	|| fail "debug network log node class is unavailable"
wait_node_class_available "almagest/TunSplitter" \
	|| fail "TunSplitter node class is unavailable"
wait_node_class_available "almagest/TunJoiner" \
	|| fail "TunJoiner node class is unavailable"
wait_graph_node "DefaultTun 1301" || fail "DefaultTun node missing"
default_tun="$(node_id_by_name "DefaultTun 1301")"
wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_log_contains "almagest/DefaultTun" "batch=128" \
	|| fail "DefaultTun did not enable the Linux VNET-header batch path"

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
log_network="$(add_node "almagest/LogNetwork" "LogNetwork 1303")"
out_network="$(add_node "almagest/OutNetwork" "OutNetwork 1304")"
splitter_one="$(add_node "almagest/TunSplitter" "TunSplitter 1305")"
joiner_one="$(add_node "almagest/TunJoiner" "TunJoiner 1306")"
splitter_two="$(add_node "almagest/TunSplitter" "TunSplitter 1307")"
joiner_two="$(add_node "almagest/TunJoiner" "TunJoiner 1308")"

cli graph apply-menu --node "$splitter_one" --json \
	'{"config":{"rules":"TRUE\nSLOT 1"}}' \
	>"$E2E_RUNTIME/configure-splitter-one.json"
cli graph apply-menu --node "$splitter_two" --json \
	'{"config":{"rules":"TRUE\nSLOT 1"}}' \
	>"$E2E_RUNTIME/configure-splitter-two.json"
cli graph apply-menu --node "$spoofer" --json \
	'{"config":{"mtu":9000}}' \
	>"$E2E_RUNTIME/configure-spoofer-mtu.json"

add_link_by_port_name "$default_tun" right Tun "$splitter_one" left Tun \
	>/tmp/e2e-default-tun-link-splitter-one.json
add_link_by_port_name "$splitter_one" right "slot 1" "$joiner_one" left Default \
	>/tmp/e2e-default-tun-link-joiner-one.json
add_link_by_port_name "$joiner_one" right Tun "$splitter_two" left Tun \
	>/tmp/e2e-default-tun-link-splitter-two.json
add_link_by_port_name "$splitter_two" right "slot 1" "$joiner_two" left Default \
	>/tmp/e2e-default-tun-link-joiner-two.json
add_link_by_port_name "$joiner_two" right Tun "$spoofer" left Tun \
	>/tmp/e2e-default-tun-link-tun.json
add_link_by_port_name "$spoofer" right Network "$log_network" left Network >/tmp/e2e-default-tun-link-log.json
add_link_by_port_name "$log_network" right Network "$out_network" left Network >/tmp/e2e-default-tun-link-out.json

wait_node_label "$splitter_one" splitting || fail "first TunSplitter did not start"
wait_node_label "$joiner_one" "joined default" || fail "first TunJoiner did not start"
wait_node_label "$splitter_two" splitting || fail "second TunSplitter did not start"
wait_node_label "$joiner_two" "joined default" || fail "second TunJoiner did not start"
wait_node_label "$spoofer" forwarding || fail "Spoofer did not start forwarding"

retry "$ALMAGEST_E2E_TIMEOUT" "public route through DefaultTun Spoofer OutNetwork chain" \
	curl_target "$public_target" /through-outnetwork-chain /tmp/e2e-default-tun-public-chain.txt \
	|| fail "public target did not work through DefaultTun/Spoofer/OutNetwork chain"
assert_body /tmp/e2e-default-tun-public-chain.txt "$public_body"

wait_log_contains "almagest/LogNetwork" "Dial tcp $public_target" \
	|| fail "debug network log did not record public target dial"
wait_log_contains "almagest/LogNetwork" "GET /through-outnetwork-chain" \
	|| fail "debug network log did not record proxied public request"
wait_log_contains "almagest/LogNetwork" "$public_body" \
	|| fail "debug network log did not record proxied public response"

# Exercise same-file MTU decreases and increases while the deep graph is
# active. The 1420-to-1428 step is the original blocked-read race. The large
# request gives Linux a GSO-sized input when IFF_VNET_HDR is available.
stable_tun_name="$(default_tun_name)"
for mtu in 1280 1420 1428 1500 9000; do
	set_default_tun_mtu "$default_tun" "$mtu"
	retry "$ALMAGEST_E2E_TIMEOUT" "DefaultTun MTU $mtu" \
		default_tun_mtu_is "$stable_tun_name" "$mtu" \
		|| fail "DefaultTun did not apply MTU $mtu"
	retry "$ALMAGEST_E2E_TIMEOUT" "large request at DefaultTun MTU $mtu" \
		curl_target_large "$public_target" "/mtu-$mtu" \
		"$E2E_RUNTIME/public-mtu-$mtu.txt" \
		|| fail "large request failed at DefaultTun MTU $mtu"
	assert_body "$E2E_RUNTIME/public-mtu-$mtu.txt" "$public_body"
	[[ "$(default_tun_name)" == "$stable_tun_name" ]] \
		|| fail "same-file MTU update replaced the native DefaultTun"
done

for carrier_mtu in 1280 1500 9000; do
	set_public_carrier_mtu "$carrier_mtu"
	retry "$ALMAGEST_E2E_TIMEOUT" "large request at carrier MTU $carrier_mtu" \
		curl_target_large "$public_target" "/carrier-mtu-$carrier_mtu" \
		"$E2E_RUNTIME/public-carrier-mtu-$carrier_mtu.txt" \
		|| fail "large request failed at carrier MTU $carrier_mtu"
	assert_body "$E2E_RUNTIME/public-carrier-mtu-$carrier_mtu.txt" "$public_body"
done
set_public_carrier_mtu 1500

# Model a native TUN that disappears while downstream VPN nodes recover and
# publish a new configuration. sysnet-linux replaces the native device inside
# shared generation state. The graph must receive a new packet source even
# though both generation wrappers report the replacement after the build.
old_tun_name="$(default_tun_name)"
ip link delete "$old_tun_name"
retry "$ALMAGEST_E2E_TIMEOUT" "old DefaultTun link removal" \
	link_is_absent "$old_tun_name" \
	|| fail "old DefaultTun link remained after deletion"
cli graph apply-menu --node "$default_tun" --json \
	'{"config":{"tunRoutes":"0.0.0.0/0"}}' \
	>/tmp/e2e-default-tun-native-replacement.json
retry "$ALMAGEST_E2E_TIMEOUT" "replacement DefaultTun link" \
	default_tun_name >/tmp/e2e-default-tun-replacement-name.txt \
	|| fail "DefaultTun did not replace its deleted native device"

retry "$ALMAGEST_E2E_TIMEOUT" "public route after native DefaultTun replacement" \
	curl_target "$public_target" /after-native-replacement \
	/tmp/e2e-default-tun-public-native-replacement.txt \
	|| fail "public target did not work after native DefaultTun replacement"
assert_body /tmp/e2e-default-tun-public-native-replacement.txt "$public_body"
wait_log_contains "almagest/LogNetwork" "GET /after-native-replacement" \
	|| fail "replacement DefaultTun traffic did not reach the existing graph path"
wait_node_label "$splitter_one" splitting \
	|| fail "first TunSplitter did not recover after native replacement"
wait_node_label "$joiner_one" "joined default" \
	|| fail "first TunJoiner did not recover after native replacement"
wait_node_label "$splitter_two" splitting \
	|| fail "second TunSplitter did not recover after native replacement"
wait_node_label "$joiner_two" "joined default" \
	|| fail "second TunJoiner did not recover after native replacement"

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
