#!/usr/bin/env bash
set -euo pipefail

: "${ALMAGEST_E2E_TIMEOUT:=45}"
source /e2e/lib/common.sh

peer_ns="alm-bypass-peer"
physical_if="almbp0"
kernel_if="wgbp0"
app_mark="0xeb9f0001"
vpn_table="51820"
inner_body="nested DefaultTun WireGuard response"
public_body="direct OutNetwork response"
inner_http_pid=""
public_http_pid=""
dns_pid=""

cleanup() {
	trap - EXIT
	stop_daemon
	stop_static_http_target "$inner_http_pid"
	stop_static_http_target "$public_http_pid"
	if [[ -n "$dns_pid" ]]; then
		kill "$dns_pid" 2>/dev/null || true
		wait "$dns_pid" 2>/dev/null || true
	fi
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
}

default_tun_name() {
	ip -j link show | jq -er \
		'.[] | select(.ifname | startswith("alm-")) | .ifname' \
		| head -1
}

link_is_absent() {
	local name="$1"
	! ip link show "$name" >/dev/null 2>&1
}

main_default_snapshot() {
	ip -j -4 route show table main default \
		| jq -cS 'sort_by(.dst, .gateway, .dev, .metric)'
}

main_default_unchanged() {
	[[ "$(main_default_snapshot)" == "$initial_default" ]]
}

route_device() {
	local destination="$1"
	local mark="${2:-}"
	local args=(route get "$destination")
	if [[ -n "$mark" ]]; then
		args+=(mark "$mark")
	fi
	ip -j -4 "${args[@]}" | jq -er '.[0].dev'
}

route_uses_device() {
	local destination="$1"
	local mark="$2"
	local device="$3"
	[[ "$(route_device "$destination" "$mark")" == "$device" ]]
}

main_routes_through_tun_are_absent() {
	local tun_name="$1"
	[[ "$(ip -j -4 route show table main dev "$tun_name" | jq 'length')" == "0" ]]
}

vpn_default_uses_tun() {
	local tun_name="$1"
	ip -j -4 route show table "$vpn_table" \
		| jq -e --arg tun_name "$tun_name" \
			'.[] | select(.dst == "default" and .dev == $tun_name)' >/dev/null
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

result_status() {
	local node="$1"
	cli graph menu --node "$node" | jq -er '
		.blocks[] | select(.id == "result")
		| .items[] | select(.id == "status")
		| .text
	'
}

result_field() {
	local node="$1"
	local field="$2"
	cli graph menu --node "$node" | jq -er --arg field "$field" '
		.blocks[] | select(.id == "result")
		| .items[] | select(.id == $field)
		| (.value // "")
	'
}

http_result_works() {
	local node="$1"
	local body="$2"
	local status
	status="$(result_status "$node")" || return 1
	if [[ "$status" == "Status: 200 OK" ]] \
		&& [[ -z "$(result_field "$node" error)" ]] \
		&& [[ "$(result_field "$node" response)" == "$body" ]]; then
		return 0
	fi
	if [[ "$status" != "Status: running..." ]]; then
		cli graph click-menu-button --node "$node" --block request --button send >/dev/null
	fi
	return 1
}

dns_result_works() {
	local node="$1"
	local status
	status="$(result_status "$node")" || return 1
	if [[ "$status" == "Status: NOERROR" ]] \
		&& [[ -z "$(result_field "$node" error)" ]] \
		&& [[ "$(result_field "$node" response)" == *"192.0.2.99"* ]]; then
		return 0
	fi
	if [[ "$status" != "Status: running..." ]]; then
		cli graph click-menu-button --node "$node" --block request --button send >/dev/null
	fi
	return 1
}

kernel_handshake_exists() {
	ip netns exec "$peer_ns" wg show "$kernel_if" latest-handshakes \
		| awk -v key="f4iMlTOU1YOfovXWCeMMjeBsc6vVqqDE/7XQeHg2oVQ=" \
			'$1 == key && $2 > 0 { found=1 } END { exit !found }'
}

host_http_request() {
	local path="$1"
	local output="$2"
	curl --fail --silent --show-error --noproxy '*' \
		--connect-timeout 2 --max-time 8 \
		"http://10.250.0.2:18091$path" --output "$output"
	[[ "$(<"$output")" == "$inner_body" ]]
}

run_concurrent_host_requests() {
	local pids=()
	local index
	for index in 1 2 3 4 5 6; do
		host_http_request "/concurrent-$index" \
			"$E2E_RUNTIME/concurrent-$index.txt" &
		pids+=("$!")
	done
	for index in "${!pids[@]}"; do
		wait "${pids[$index]}" \
			|| fail "concurrent host request $((index + 1)) failed"
	done
}

assert_route_isolation() {
	local tun_name="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "main table without $tun_name" \
		main_routes_through_tun_are_absent "$tun_name" \
		|| fail "a route through $tun_name leaked into the main table"
	retry "$ALMAGEST_E2E_TIMEOUT" "VPN-table default through $tun_name" \
		vpn_default_uses_tun "$tun_name" \
		|| fail "the VPN-table default does not use $tun_name"
	retry "$ALMAGEST_E2E_TIMEOUT" "unmarked endpoint route through $tun_name" \
		route_uses_device 203.0.113.2 "" "$tun_name" \
		|| fail "unmarked traffic does not select $tun_name"
	retry "$ALMAGEST_E2E_TIMEOUT" "marked endpoint route through $physical_if" \
		route_uses_device 203.0.113.2 "$app_mark" "$physical_if" \
		|| fail "application-marked traffic does not bypass $tun_name"
	main_default_unchanged \
		|| fail "DefaultTun changed the original main-table default"
}

trap cleanup EXIT

cat >"$E2E_RUNTIME/dnsmasq.conf" <<'EOF'
port=5353
listen-address=10.250.0.2
bind-interfaces
no-resolv
no-hosts
cache-size=0
log-queries
address=/nested-bypass.e2e.test/192.0.2.99
EOF
ip netns exec "$peer_ns" dnsmasq --no-daemon \
	--conf-file="$E2E_RUNTIME/dnsmasq.conf" \
	>"$E2E_RUNTIME/dnsmasq.stdout" 2>"$E2E_RUNTIME/dnsmasq.stderr" &
dns_pid="$!"
retry "$ALMAGEST_E2E_TIMEOUT" "kernel-peer DNS listener" \
	ip netns exec "$peer_ns" nc -z 10.250.0.2 5353 \
	|| fail "kernel-peer DNS service did not start"

inner_http_pid="$(start_static_http_target_in_netns \
	"$peer_ns" 10.250.0.2 18091 "$inner_body" "$E2E_RUNTIME/inner-http.log")"
public_http_pid="$(start_static_http_target_in_netns \
	"$peer_ns" 203.0.113.2 18092 "$public_body" "$E2E_RUNTIME/public-http.log")"

initial_default="$(main_default_snapshot)"
[[ "$(route_device 203.0.113.2)" == "$physical_if" ]] \
	|| fail "the test endpoint does not use the synthetic physical default"

start_daemon

for name in \
	"DefaultTun entry" \
	"Original OutDns" \
	"Application VTun" \
	"Shared TunJoiner" \
	"Route TunSplitter" \
	"WireGuard tunnel" \
	"WireGuard OutNetwork" \
	"Tunnel HTTP client" \
	"Tunnel DNS client" \
	"Tunnel DNS query" \
	"Direct public HTTP client" \
	"Direct OutNetwork"; do
	wait_graph_node "$name" || fail "$name node is missing"
done

default_tun="$(node_id_by_name "DefaultTun entry")"
out_dns="$(node_id_by_name "Original OutDns")"
app_vtun="$(node_id_by_name "Application VTun")"
joiner="$(node_id_by_name "Shared TunJoiner")"
splitter="$(node_id_by_name "Route TunSplitter")"
wireguard="$(node_id_by_name "WireGuard tunnel")"
tunnel_http="$(node_id_by_name "Tunnel HTTP client")"
tunnel_dns_client="$(node_id_by_name "Tunnel DNS client")"
tunnel_dns_query="$(node_id_by_name "Tunnel DNS query")"
direct_http="$(node_id_by_name "Direct public HTTP client")"

wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_node_label "$joiner" "joined default + 1 secondary" \
	|| fail "TunJoiner did not attach both input TUNs"
wait_node_label "$splitter" splitting || fail "TunSplitter did not start"
wait_node_label "$wireguard" up || fail "WireGuard did not become up"
wait_node_label "$app_vtun" nested-bypass-app || fail "application VTun did not become ready"
wait_node_label "$tunnel_dns_client" "1 DNS server" \
	|| fail "tunnel DNS client did not become ready"

retry "$ALMAGEST_E2E_TIMEOUT" "native DefaultTun interface" \
	default_tun_name >"$E2E_RUNTIME/default-tun-name.txt" \
	|| fail "native DefaultTun interface was not created"
tun_name="$(<"$E2E_RUNTIME/default-tun-name.txt")"

retry "$ALMAGEST_E2E_TIMEOUT" "effective DefaultTun address" \
	effective_field_contains "$default_tun" tunAddrs "10.250.0.1/32" \
	|| fail "WireGuard did not propagate its address to DefaultTun"
retry "$ALMAGEST_E2E_TIMEOUT" "effective DefaultTun route" \
	effective_field_contains "$default_tun" tunRoutes "0.0.0.0/0" \
	|| fail "WireGuard did not propagate its default route to DefaultTun"
retry "$ALMAGEST_E2E_TIMEOUT" "effective DefaultTun MTU" \
	effective_field_contains "$default_tun" mtu "1440" \
	|| fail "WireGuard did not propagate its MTU to DefaultTun"
assert_route_isolation "$tun_name"

retry "$ALMAGEST_E2E_TIMEOUT" "direct OutNetwork HTTP request" \
	http_result_works "$direct_http" "$public_body" \
	|| fail "OutNetwork HTTP could not bypass DefaultTun"
retry 90 "application-VTun HTTP through WireGuard" \
	http_result_works "$tunnel_http" "$inner_body" \
	|| fail "application VTun could not reach the WireGuard peer"
retry 90 "application-VTun DNS through WireGuard" \
	dns_result_works "$tunnel_dns_query" \
	|| fail "application VTun DNS could not reach the WireGuard peer"
retry 90 "host HTTP through DefaultTun and WireGuard" \
	host_http_request /from-default-tun "$E2E_RUNTIME/default-tun-http.txt" \
	|| fail "host traffic could not cross DefaultTun and WireGuard"
retry "$ALMAGEST_E2E_TIMEOUT" "kernel WireGuard handshake" kernel_handshake_exists \
	|| fail "kernel peer did not report a WireGuard handshake"

run_concurrent_host_requests
retry "$ALMAGEST_E2E_TIMEOUT" "WireGuard transport log" \
	log_contains "almagest/LogNetwork" "203.0.113.2:51881" \
	|| fail "WireGuard transport did not use its LogNetwork path"
retry "$ALMAGEST_E2E_TIMEOUT" "direct HTTP transport log" \
	log_contains "almagest/LogNetwork" "203.0.113.2:18092" \
	|| fail "direct HTTP did not use its LogNetwork path"

# Removing the default input must not stop the secondary VTun or WireGuard.
cli graph delete-node --id "$default_tun" >"$E2E_RUNTIME/delete-default-tun.json"
retry "$ALMAGEST_E2E_TIMEOUT" "DefaultTun interface removal" \
	link_is_absent "$tun_name" \
	|| fail "DefaultTun interface remained after node deletion"
retry "$ALMAGEST_E2E_TIMEOUT" "main default after DefaultTun deletion" \
	main_default_unchanged \
	|| fail "DefaultTun deletion did not preserve the main default"
wait_node_label "$joiner" "waiting for default" \
	|| fail "TunJoiner did not detach the deleted default input"
retry 90 "secondary VTun HTTP without DefaultTun" \
	http_result_works "$tunnel_http" "$inner_body" \
	|| fail "secondary VTun stopped after DefaultTun deletion"
retry 90 "secondary VTun DNS without DefaultTun" \
	dns_result_works "$tunnel_dns_query" \
	|| fail "secondary VTun DNS stopped after DefaultTun deletion"

# Recreate the native input and attach it to the running joiner.
default_tun="$(add_node "almagest/DefaultTun" "DefaultTun rebuilt")"
wait_node_label "$default_tun" ready || fail "recreated DefaultTun did not become ready"
add_link_by_port_name "$default_tun" right Tun "$joiner" left Default \
	>"$E2E_RUNTIME/rebuild-tun-link.json"
add_link_by_port_name "$default_tun" right DNS "$out_dns" left DNS \
	>"$E2E_RUNTIME/rebuild-dns-link.json"
wait_node_label "$joiner" "joined default + 1 secondary" \
	|| fail "TunJoiner did not attach the recreated DefaultTun"
retry "$ALMAGEST_E2E_TIMEOUT" "recreated DefaultTun address" \
	effective_field_contains "$default_tun" tunAddrs "10.250.0.1/32" \
	|| fail "recreated DefaultTun did not receive the WireGuard address"
retry "$ALMAGEST_E2E_TIMEOUT" "recreated DefaultTun MTU" \
	effective_field_contains "$default_tun" mtu "1440" \
	|| fail "recreated DefaultTun did not receive the WireGuard MTU"
retry "$ALMAGEST_E2E_TIMEOUT" "recreated DefaultTun interface" \
	default_tun_name >"$E2E_RUNTIME/rebuilt-default-tun-name.txt" \
	|| fail "recreated DefaultTun interface was not created"
tun_name="$(<"$E2E_RUNTIME/rebuilt-default-tun-name.txt")"
assert_route_isolation "$tun_name"
retry 90 "host HTTP after DefaultTun recreation" \
	host_http_request /after-recreate "$E2E_RUNTIME/recreated-default-tun-http.txt" \
	|| fail "host traffic did not recover after DefaultTun recreation"

assert_log_not_contains "almagest/DefaultTun" "configure default TUN failed"

stop_daemon
retry "$ALMAGEST_E2E_TIMEOUT" "main default after daemon shutdown" \
	main_default_unchanged \
	|| fail "daemon shutdown did not preserve the original main default"

grep -F "GET /from-default-tun" "$E2E_RUNTIME/inner-http.log" >/dev/null \
	|| fail "inner HTTP server did not receive DefaultTun traffic"
grep -F "GET /from-application-vtun" "$E2E_RUNTIME/inner-http.log" >/dev/null \
	|| fail "inner HTTP server did not receive application-VTun traffic"
grep -F "GET /from-outnetwork" "$E2E_RUNTIME/public-http.log" >/dev/null \
	|| fail "public HTTP server did not receive OutNetwork traffic"
grep -Fi "nested-bypass.e2e.test" "$E2E_RUNTIME/dnsmasq.stdout" >/dev/null \
	|| grep -Fi "nested-bypass.e2e.test" "$E2E_RUNTIME/dnsmasq.stderr" >/dev/null \
	|| fail "kernel-peer DNS server did not receive the tunnel query"

log "case passed"
