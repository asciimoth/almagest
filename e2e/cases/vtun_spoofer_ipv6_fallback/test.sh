#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

target_body="IPv4 fallback reached"
target_log="$E2E_RUNTIME/target.log"
target_pid=""
dns_pid=""
blackhole_ns="alm-ds-blackhole"

cleanup() {
	stop_static_http_target "$target_pid"
	if [[ -n "$dns_pid" ]]; then
		kill "$dns_pid" 2>/dev/null || true
		wait "$dns_pid" 2>/dev/null || true
	fi
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-host.sh" ]]; then
		"$E2E_RUNTIME/cleanup-host.sh"
	fi
}

menu_text() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --name "$node" | jq -er \
		--arg block "$block" \
		--arg field "$field" \
		'
		.blocks[]
		| select(.id == $block)
		| .items[]
		| select(.id == $field)
		| (.value // .text // "")
		'
}

target_request_count_at_least() {
	local want="$1"
	local got=0
	if [[ -f "$target_log" ]]; then
		got="$(wc -l <"$target_log")"
	fi
	((got >= want))
}

http_result_ready() {
	local want_count="$1"
	local status
	local err
	local response
	status="$(menu_text "Fallback Client" result status)" || return 1
	err="$(menu_text "Fallback Client" result error)" || return 1
	response="$(menu_text "Fallback Client" result response)" || return 1
	[[ "$status" == "Status: 200 OK" && -z "$err" && "$response" == "$target_body" ]] \
		&& target_request_count_at_least "$want_count"
}

blackhole_packet_count() {
	ip netns exec "$blackhole_ns" nft -j list chain ip6 almagest_e2e input \
		| jq -er '[.. | objects | .counter? | select(type == "object") | .packets] | add // 0'
}

send_request_and_wait() {
	local request_number="$1"
	local started
	local elapsed_ms
	started="$(date +%s%N)"
	cli graph click-menu-button --name "Fallback Client" --block request --button send >/dev/null
	retry 5 "dual-stack request $request_number IPv4 fallback" \
		http_result_ready "$request_number" \
		|| fail "request $request_number did not use IPv4 fallback in five seconds"
	elapsed_ms="$((($(date +%s%N) - started) / 1000000))"
	((elapsed_ms < 5000)) \
		|| fail "request $request_number took ${elapsed_ms}ms; expected less than 5000ms"
	log "request $request_number completed through IPv4 fallback in ${elapsed_ms}ms"
}

trap cleanup EXIT

target_pid="$(start_static_http_target 192.0.2.80 18080 "$target_body" "$target_log")"

dns_conf="$E2E_RUNTIME/dnsmasq.conf"
cat >"$dns_conf" <<EOF
port=53
listen-address=192.0.2.53
bind-interfaces
no-resolv
no-hosts
local-ttl=60
host-record=dual-stack-fallback.test,192.0.2.80,2001:db8:dead::80
EOF
dnsmasq --no-daemon --conf-file="$dns_conf" \
	>"$E2E_RUNTIME/dnsmasq.log" 2>"$E2E_RUNTIME/dnsmasq.err" &
dns_pid="$!"

retry "$ALMAGEST_E2E_TIMEOUT" "dual-stack DNS fixture" \
	bash -c 'dig +short @192.0.2.53 dual-stack-fallback.test A | grep -Fx 192.0.2.80 >/dev/null && dig +short @192.0.2.53 dual-stack-fallback.test AAAA | grep -Fx 2001:db8:dead::80 >/dev/null' \
	|| fail "DNS fixture did not return both address families"

set +e
timeout 1 nc -6 -z -w 10 2001:db8:dead::80 18080
blackhole_status="$?"
set -e
[[ "$blackhole_status" == 124 ]] \
	|| fail "IPv6 fixture returned status $blackhole_status instead of staying pending"
initial_blackhole_packets="$(blackhole_packet_count)"
((initial_blackhole_packets > 0)) \
	|| fail "IPv6 blackhole did not count the baseline TCP attempt"

start_daemon

for node in \
	"Fallback Client" \
	"Dual Stack VTun" \
	"Fallback Joiner" \
	"Fallback Spoofer" \
	"Fallback OutNetwork"; do
	wait_graph_node "$node" || fail "$node node missing"
done

vtun="$(node_id_by_name "Dual Stack VTun")"
joiner="$(node_id_by_name "Fallback Joiner")"
spoofer="$(node_id_by_name "Fallback Spoofer")"
wait_node_label "$vtun" dual-stack-fallback-vtun \
	|| fail "VTun did not become ready"
wait_node_label "$joiner" "joined default" \
	|| fail "TunJoiner did not attach the VTun as its default input"
wait_node_label "$spoofer" forwarding \
	|| fail "Spoofer did not start forwarding"

families="$(menu_text "Dual Stack VTun" effective addressFamilies)"
[[ "$families" == $'ipv4\nipv6' ]] \
	|| fail "VTun effective address families are '$families', expected IPv4 and IPv6"

for request_number in 1 2 3 4 5; do
	send_request_and_wait "$request_number"
done

final_blackhole_packets="$(blackhole_packet_count)"
((final_blackhole_packets > initial_blackhole_packets)) \
	|| fail "HTTP requests did not attempt the blackholed IPv6 address"
[[ "$(wc -l <"$target_log")" == 5 ]] \
	|| fail "IPv4 target did not receive exactly five requests"

log "case passed"
