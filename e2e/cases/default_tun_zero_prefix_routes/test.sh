#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

app_mark="0xeb9f0001"
vpn_table="51820"

cleanup() {
	trap - EXIT
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
}

default_tun_name() {
	ip -j link show | jq -er \
		'.[] | select(.ifname | startswith("alm-")) | .ifname' \
		| head -1
}

main_defaults_snapshot() {
	{
		ip -j -4 route show table main default
		ip -j -6 route show table main default
	} | jq -csS 'map(sort_by(.dst, .gateway, .dev, .metric))'
}

main_defaults_unchanged() {
	[[ "$(main_defaults_snapshot)" == "$initial_defaults" ]]
}

route_device() {
	local family="$1"
	local destination="$2"
	local mark="${3:-}"
	local args=(route get "$destination")
	if [[ -n "$mark" ]]; then
		args+=(mark "$mark")
	fi
	ip -j "-$family" "${args[@]}" | jq -er '.[0].dev'
}

route_uses_device() {
	local family="$1"
	local destination="$2"
	local mark="$3"
	local device="$4"
	[[ "$(route_device "$family" "$destination" "$mark")" == "$device" ]]
}

main_routes_through_tun_are_absent() {
	local family="$1"
	local tun_name="$2"
	[[ "$(ip -j "-$family" route show table main dev "$tun_name" | jq 'length')" == "0" ]]
}

vpn_default_uses_tun() {
	local family="$1"
	local tun_name="$2"
	ip -j "-$family" route show table "$vpn_table" \
		| jq -e --arg tun_name "$tun_name" \
			'.[] | select(.dst == "default" and .dev == $tun_name)' >/dev/null
}

vpn_rule_exists() {
	local family="$1"
	ip -j "-$family" rule show \
		| jq -e --arg table "$vpn_table" \
			'.[] | select((.table | tostring) == $table)' >/dev/null
}

vpn_rule_is_absent() {
	local family="$1"
	! vpn_rule_exists "$family"
}

effective_field() {
	local node="$1"
	local field="$2"
	cli graph menu --node "$node" | jq -er --arg field "$field" '
		.blocks[] | select(.id == "effective")
		| .items[] | select(.id == $field)
		| (.value // .text // "")
	'
}

effective_field_contains() {
	local node="$1"
	local field="$2"
	local value="$3"
	[[ "$(effective_field "$node" "$field")" == *"$value"* ]]
}

apply_default_tun_config() {
	local node="$1"
	local addrs="$2"
	local routes="$3"
	jq -cn --arg addrs "$addrs" --arg routes "$routes" \
		'{config:{tunAddrs:$addrs,tunRoutes:$routes}}' \
		| cli graph apply-menu --node "$node" \
			>"$E2E_RUNTIME/apply-default-tun.json"
}

assert_route_isolation() {
	local tun_name="$1"

	retry "$ALMAGEST_E2E_TIMEOUT" "IPv4 main table without $tun_name" \
		main_routes_through_tun_are_absent 4 "$tun_name" \
		|| fail "IPv4 route through $tun_name leaked into the main table"
	retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 main table without $tun_name" \
		main_routes_through_tun_are_absent 6 "$tun_name" \
		|| fail "IPv6 route through $tun_name leaked into the main table"

	retry "$ALMAGEST_E2E_TIMEOUT" "IPv4 VPN-table default through $tun_name" \
		vpn_default_uses_tun 4 "$tun_name" \
		|| fail "IPv4 VPN-table default does not use $tun_name"
	retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 VPN-table default through $tun_name" \
		vpn_default_uses_tun 6 "$tun_name" \
		|| fail "IPv6 VPN-table default does not use $tun_name"

	retry "$ALMAGEST_E2E_TIMEOUT" "unmarked IPv4 route through $tun_name" \
		route_uses_device 4 1.1.1.1 "" "$tun_name" \
		|| fail "unmarked IPv4 traffic does not use $tun_name"
	retry "$ALMAGEST_E2E_TIMEOUT" "unmarked IPv6 route through $tun_name" \
		route_uses_device 6 2606:4700:4700::1111 "" "$tun_name" \
		|| fail "unmarked IPv6 traffic does not use $tun_name"

	retry "$ALMAGEST_E2E_TIMEOUT" "marked IPv4 route through $physical_v4" \
		route_uses_device 4 1.1.1.1 "$app_mark" "$physical_v4" \
		|| fail "marked IPv4 traffic did not bypass $tun_name"
	retry "$ALMAGEST_E2E_TIMEOUT" "marked IPv6 route through $physical_v6" \
		route_uses_device 6 2606:4700:4700::1111 "$app_mark" "$physical_v6" \
		|| fail "marked IPv6 traffic did not bypass $tun_name"

	main_defaults_unchanged \
		|| fail "DefaultTun changed an original main-table default route"
}

trap cleanup EXIT

initial_defaults="$(main_defaults_snapshot)"
physical_v4="$(route_device 4 1.1.1)"
physical_v6="$(route_device 6 2606:4700:4700::1111)"

start_daemon

wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_graph_node "DefaultTun route isolation" \
	|| fail "DefaultTun regression node is missing"

default_tun="$(node_id_by_name "DefaultTun route isolation")"
wait_node_label "$default_tun" ready \
	|| fail "DefaultTun did not accept dual-stack routes"

retry "$ALMAGEST_E2E_TIMEOUT" "native DefaultTun interface" \
	default_tun_name >"$E2E_RUNTIME/default-tun-name.txt" \
	|| fail "native DefaultTun interface was not created"
tun_name="$(<"$E2E_RUNTIME/default-tun-name.txt")"

assert_route_isolation "$tun_name"
effective_field_contains "$default_tun" tunRoutes "0.0.0.0/0" \
	|| fail "effective config lost the IPv4 default route suggestion"
effective_field_contains "$default_tun" tunRoutes "2606:4700:4700::1111/128" \
	|| fail "effective config lost the IPv6 host route suggestion"

# Change only route suggestions. They must stay in the policy table.
apply_default_tun_config "$default_tun" \
	"10.66.0.1/24
fd66::1/64" \
	"0.0.0.0/0
9.9.9.9/32
::/0
2620:fe::fe/128"
wait_node_label "$default_tun" ready \
	|| fail "DefaultTun did not accept a route-only live update"
assert_route_isolation "$tun_name"
effective_field_contains "$default_tun" tunRoutes "9.9.9.9/32" \
	|| fail "effective config did not apply the route-only update"

# Change only subnet addresses. Kernel-connected routes created by this call
# must also be removed from main.
apply_default_tun_config "$default_tun" \
	"10.67.0.1/24
fd67::1/64" \
	"0.0.0.0/0
9.9.9.9/32
::/0
2620:fe::fe/128"
wait_node_label "$default_tun" ready \
	|| fail "DefaultTun did not accept an address-only live update"
assert_route_isolation "$tun_name"
effective_field_contains "$default_tun" tunAddrs "10.67.0.1/24" \
	|| fail "effective config did not apply the address-only update"

# Delete and recreate the unique runtime node. Main-table defaults must stay
# intact through both transitions.
cli graph delete-node --id "$default_tun" >"$E2E_RUNTIME/delete-default-tun.json"
retry "$ALMAGEST_E2E_TIMEOUT" "DefaultTun interface removal" \
	sh -c "! ip link show '$tun_name' >/dev/null 2>&1" \
	|| fail "DefaultTun interface remained after node deletion"
retry "$ALMAGEST_E2E_TIMEOUT" "IPv4 policy-rule removal" vpn_rule_is_absent 4 \
	|| fail "IPv4 DefaultTun policy rule remained after node deletion"
retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 policy-rule removal" vpn_rule_is_absent 6 \
	|| fail "IPv6 DefaultTun policy rule remained after node deletion"
retry "$ALMAGEST_E2E_TIMEOUT" "main routes after node deletion" main_defaults_unchanged \
	|| fail "node deletion did not restore the original defaults"

default_tun="$(add_node "almagest/DefaultTun" "DefaultTun route isolation rebuilt")"
wait_node_label "$default_tun" ready \
	|| fail "recreated DefaultTun did not become ready"
apply_default_tun_config "$default_tun" \
	"10.68.0.1/24
fd68::1/64" \
	"0.0.0.0/0
8.8.8.8/32
::/0
2001:4860:4860::8888/128"
wait_node_label "$default_tun" ready \
	|| fail "recreated DefaultTun did not accept config"
retry "$ALMAGEST_E2E_TIMEOUT" "recreated DefaultTun interface" \
	default_tun_name >"$E2E_RUNTIME/rebuilt-default-tun-name.txt" \
	|| fail "recreated DefaultTun interface was not created"
tun_name="$(<"$E2E_RUNTIME/rebuilt-default-tun-name.txt")"
assert_route_isolation "$tun_name"

assert_log_not_contains "almagest/DefaultTun" "configure default TUN failed"

stop_daemon
retry "$ALMAGEST_E2E_TIMEOUT" "main routes after daemon shutdown" main_defaults_unchanged \
	|| fail "daemon shutdown did not preserve the original defaults"
retry "$ALMAGEST_E2E_TIMEOUT" "IPv4 policy-rule cleanup" vpn_rule_is_absent 4 \
	|| fail "daemon shutdown left an IPv4 policy rule"
retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 policy-rule cleanup" vpn_rule_is_absent 6 \
	|| fail "daemon shutdown left an IPv6 policy rule"

log "case passed"
