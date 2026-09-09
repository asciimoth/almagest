#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

cleanup() {
	stop_daemon
}

default_tun_name() {
	ip -j link show | jq -er \
		'.[] | select(.ifname | startswith("alm-")) | .ifname' \
		| head -1
}

default_route_uses_tun() {
	local family="$1"
	local tun_name="$2"
	ip -j "-$family" route show table main \
		| jq -e \
			--arg tun_name "$tun_name" \
			'.[] | select(.dst == "default" and .dev == $tun_name and .protocol == "static")' \
			>/dev/null
}

trap cleanup EXIT

start_daemon

wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_graph_node "DefaultTun zero routes" \
	|| fail "DefaultTun regression node is missing"

default_tun="$(node_id_by_name "DefaultTun zero routes")"
wait_node_label "$default_tun" ready \
	|| fail "DefaultTun did not accept zero-prefix routes"

retry "$ALMAGEST_E2E_TIMEOUT" "native DefaultTun interface" \
	default_tun_name >/tmp/e2e-default-tun-zero-route-name.txt \
	|| fail "native DefaultTun interface was not created"
tun_name="$(</tmp/e2e-default-tun-zero-route-name.txt)"

retry "$ALMAGEST_E2E_TIMEOUT" "IPv4 default route on $tun_name" \
	default_route_uses_tun 4 "$tun_name" \
	|| fail "IPv4 zero-prefix route was not installed on $tun_name"
retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 default route on $tun_name" \
	default_route_uses_tun 6 "$tun_name" \
	|| fail "IPv6 zero-prefix route was not installed on $tun_name"

assert_log_not_contains "almagest/DefaultTun" "configure default TUN failed"

log "case passed"
