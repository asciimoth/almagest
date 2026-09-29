#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

public_target="192.0.2.10:18096"
lan_target="10.210.0.10:18096"
ethernet_body="network migration ethernet response"
wifi_body="network migration wifi response"
ethernet_log="$E2E_RUNTIME/ethernet-target.log"
wifi_log="$E2E_RUNTIME/wifi-target.log"
ethernet_pid=""
wifi_pid=""

cleanup() {
	stop_static_http_target "$ethernet_pid"
	stop_static_http_target "$wifi_pid"
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
}

clear_physical_routes() {
	while ip route del default 2>/dev/null; do :; done
	ip route del 10.210.0.0/24 2>/dev/null || true
}

use_no_network() {
	clear_physical_routes
	ip link set almeth0 down
	ip link set almwifi0 down
}

use_ethernet() {
	use_no_network
	ip link set almeth0 up
	ip route add default via 198.51.100.2 dev almeth0 onlink
	ip route add 10.210.0.0/24 via 198.51.100.2 dev almeth0 onlink
}

use_wifi() {
	use_no_network
	ip link set almwifi0 up
	ip route add default via 203.0.113.2 dev almwifi0 onlink
	ip route add 10.210.0.0/24 via 203.0.113.2 dev almwifi0 onlink
}

curl_target() {
	local target="$1"
	local path="$2"
	local output="$3"
	shift 3
	curl --fail --silent --show-error --connect-timeout 1 --max-time 3 \
		"$@" "http://$target$path" --output "$output"
}

body_is() {
	local file="$1"
	local expected="$2"
	[[ -f "$file" && "$(cat "$file")" == "$expected" ]]
}

wait_request() {
	local description="$1"
	local target="$2"
	local path="$3"
	local expected="$4"
	shift 4
	local output="$E2E_RUNTIME/${path//\//-}.body"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		curl_target "$target" "$path" "$output" "$@" \
		|| fail "$description did not recover"
	body_is "$output" "$expected" \
		|| fail "$description returned an unexpected response: $(cat "$output" 2>/dev/null || true)"
}

request_eventually_fails() {
	local description="$1"
	local target="$2"
	local path="$3"
	shift 3
	local output="$E2E_RUNTIME/unexpected.body"
	if curl_target "$target" "$path" "$output" "$@" \
		>"$E2E_RUNTIME/unexpected.stdout" 2>"$E2E_RUNTIME/expected-failure.log"; then
		return 1
	fi
	return 0
}

wait_no_network_failure() {
	local description="$1"
	local target="$2"
	local path="$3"
	shift 3
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		request_eventually_fails "$description" "$target" "$path" "$@" \
		|| fail "$description continued to reach a target"
}

run_proxy_migration_cycle() {
	local iteration="$1"
	local socks=(--socks5-hostname 127.0.0.1:1188)

	use_ethernet
	wait_request "proxy ethernet iteration $iteration" "$public_target" \
		"/proxy-$iteration-ethernet" "$ethernet_body" "${socks[@]}"
	use_no_network
	wait_no_network_failure "proxy no-network after ethernet iteration $iteration" \
		"$public_target" "/proxy-$iteration-no-network-ethernet" "${socks[@]}"
	use_wifi
	wait_request "proxy Wi-Fi iteration $iteration" "$public_target" \
		"/proxy-$iteration-wifi" "$wifi_body" "${socks[@]}"
	use_no_network
	wait_no_network_failure "proxy no-network after Wi-Fi iteration $iteration" \
		"$public_target" "/proxy-$iteration-no-network-wifi" "${socks[@]}"
}

run_default_tun_migration_cycle() {
	local iteration="$1"
	local mode="$2"

	use_ethernet
	wait_request "$mode public ethernet iteration $iteration" "$public_target" \
		"/$mode-$iteration-public-ethernet" "$ethernet_body"
	wait_request "$mode LAN ethernet iteration $iteration" "$lan_target" \
		"/$mode-$iteration-lan-ethernet" "$ethernet_body"
	use_no_network
	wait_no_network_failure "$mode public no-network iteration $iteration" \
		"$public_target" "/$mode-$iteration-public-no-network"
	wait_no_network_failure "$mode LAN no-network iteration $iteration" \
		"$lan_target" "/$mode-$iteration-lan-no-network"
	use_wifi
	wait_request "$mode public Wi-Fi iteration $iteration" "$public_target" \
		"/$mode-$iteration-public-wifi" "$wifi_body"
	wait_request "$mode LAN Wi-Fi iteration $iteration" "$lan_target" \
		"/$mode-$iteration-lan-wifi" "$wifi_body"
}

trap cleanup EXIT

ethernet_pid="$(start_static_http_target_in_netns \
	alm-mig-ethernet 0.0.0.0 18096 "$ethernet_body" "$ethernet_log")"
wifi_pid="$(start_static_http_target_in_netns \
	alm-mig-wifi 0.0.0.0 18096 "$wifi_body" "$wifi_log")"

start_daemon
wait_graph_node "Migration proxy" || fail "migration proxy node missing"
wait_graph_node "Migration OutNetwork" || fail "migration OutNetwork node missing"
wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1188" \
	|| fail "migration proxy did not start"

for iteration in 1 2 3; do
	run_proxy_migration_cycle "$iteration"
done

# Construct the realistic path only after the small graph has passed.
use_ethernet
for class in DefaultTun TunSplitter TunJoiner Spoofer LogNetwork; do
	wait_node_class_available "almagest/$class" \
		|| fail "$class node class is unavailable"
done

splitter="$(add_node "almagest/TunSplitter" "Migration TunSplitter")"
joiner="$(add_node "almagest/TunJoiner" "Migration TunJoiner")"
spoofer="$(add_node "almagest/Spoofer" "Migration Spoofer")"
log_network="$(add_node "almagest/LogNetwork" "Migration LogNetwork")"
out_network="$(node_id_by_name "Migration OutNetwork")"

cli graph apply-menu --node "$splitter" --json \
	'{"config":{"rules":"TRUE\nSLOT 1"}}' \
	>"$E2E_RUNTIME/configure-splitter.json"
cli graph apply-menu --node "$spoofer" --json \
	'{"config":{"mtu":1500,"queueLength":1024}}' \
	>"$E2E_RUNTIME/configure-spoofer.json"

add_link_by_port_name "$splitter" right "slot 1" "$joiner" left Default \
	>"$E2E_RUNTIME/link-splitter-joiner.json"
add_link_by_port_name "$joiner" right Tun "$spoofer" left Tun \
	>"$E2E_RUNTIME/link-joiner-spoofer.json"
add_link_by_port_name "$spoofer" right Network "$log_network" left Network \
	>"$E2E_RUNTIME/link-spoofer-log.json"
add_link_by_port_name "$log_network" right Network "$out_network" left Network \
	>"$E2E_RUNTIME/link-log-out.json"

default_tun="$(add_node "almagest/DefaultTun" "Migration DefaultTun")"
add_link_by_port_name "$default_tun" right Tun "$splitter" left Tun \
	>"$E2E_RUNTIME/link-default-tun-splitter.json"

wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_node_label "$splitter" splitting || fail "TunSplitter did not start"
wait_node_label "$joiner" "joined default" || fail "TunJoiner did not start"
wait_node_label "$spoofer" forwarding || fail "Spoofer did not start"

for iteration in 1 2 3; do
	run_default_tun_migration_cycle "$iteration" non-strict
done
assert_log_not_contains "almagest/LogNetwork" "GET /non-strict-3-lan-wifi"

cli graph apply-menu --node "$default_tun" --json \
	'{"config":{"strict":true}}' >"$E2E_RUNTIME/default-tun-strict.json"
for iteration in 1 2; do
	run_default_tun_migration_cycle "$iteration" strict
done

wait_log_contains "almagest/LogNetwork" "GET /non-strict-3-public-wifi" \
	|| fail "public request did not pass through the realistic network graph"
wait_log_contains "almagest/LogNetwork" "GET /strict-2-lan-wifi" \
	|| fail "strict LAN request did not pass through the realistic network graph"

grep -F "GET /proxy-3-ethernet" "$ethernet_log" >/dev/null \
	|| fail "ethernet target did not receive the final simple request"
grep -F "GET /strict-2-public-wifi" "$wifi_log" >/dev/null \
	|| fail "Wi-Fi target did not receive the final realistic request"

log "case passed"
