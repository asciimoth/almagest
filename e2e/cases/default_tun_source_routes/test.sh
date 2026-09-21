#!/usr/bin/env bash
set -euo pipefail

: "${ALMAGEST_E2E_TIMEOUT:=45}"
source /e2e/lib/common.sh

peer_ns="alm-source-peer"
physical_if="almsr0"
vpn_table="51820"
app_mark="0xeb9f0001"

default_v4_local="10.250.0.1"
default_v4_remote="10.250.0.2"
overlay_v4_local="100.100.0.1"
overlay_v4_remote="100.100.0.2"
default_v6_local="fd10:250::1"
default_v6_remote="fd10:250::2"
overlay_v6_local="fd7a:115c:a1e0::1"
overlay_v6_remote="fd7a:115c:a1e0::2"

default_client_public="f4iMlTOU1YOfovXWCeMMjeBsc6vVqqDE/7XQeHg2oVQ="
overlay_client_public="iPdQZNB08/wCyBwSw7rTAET5KidcvhELeBdkV5XTjF4="

service_pids=()

cleanup() {
	trap - EXIT
	stop_daemon
	local pid
	for pid in "${service_pids[@]}"; do
		stop_static_http_target "$pid"
	done
	if [[ -x "$E2E_RUNTIME/cleanup-netns.sh" ]]; then
		"$E2E_RUNTIME/cleanup-netns.sh"
	fi
}

default_tun_name() {
	ip -j link show | jq -er \
		'.[] | select(.ifname | startswith("alm-")) | .ifname' \
		| head -1
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

effective_field_lacks() {
	local node="$1"
	local field="$2"
	local value="$3"
	[[ "$(effective_field "$node" "$field")" != *"$value"* ]]
}

route_lookup_matches() {
	local family="$1"
	local destination="$2"
	local device="$3"
	local source="$4"
	ip -j "$family" route get "$destination" | jq -e \
		--arg device "$device" --arg source "$source" \
		'.[0].dev == $device and .[0].prefsrc == $source' >/dev/null
}

marked_route_uses_physical_network() {
	local endpoint="$1"
	ip -j -4 route get "$endpoint" mark "$app_mark" | jq -e \
		--arg device "$physical_if" '.[0].dev == $device' >/dev/null
}

main_routes_through_tun_are_absent() {
	local tun_name="$1"
	[[ "$(ip -j -4 route show table main dev "$tun_name" | jq 'length')" == "0" ]] \
		&& [[ "$(ip -j -6 route show table main dev "$tun_name" | jq 'length')" == "0" ]]
}

ipv6_tun_address_is_ready() {
	local tun_name="$1"
	local address="$2"
	ip -j -details -6 addr show dev "$tun_name" | jq -e \
		--arg address "$address" '
		any(.[].addr_info[]?;
			.family == "inet6"
			and .local == $address
			and (.nodad // false)
			and ((.tentative // false) | not)
			and ((.dadfailed // false) | not)
		)' >/dev/null
}

vpn_source_route_exists() {
	local family="$1"
	local destination="$2"
	local device="$3"
	local source="$4"
	ip -j "$family" route show table "$vpn_table" "$destination" | jq -e \
		--arg device "$device" --arg source "$source" \
		'.[] | select(.dev == $device and .prefsrc == $source)' >/dev/null
}

kernel_handshake_exists() {
	local interface="$1"
	local public_key="$2"
	ip netns exec "$peer_ns" wg show "$interface" latest-handshakes \
		| awk -v key="$public_key" \
			'$1 == key && $2 > 0 { found=1 } END { exit !found }'
}

http_url() {
	local address="$1"
	local port="$2"
	local path="$3"
	if [[ "$address" == *:* ]]; then
		printf 'http://[%s]:%s%s' "$address" "$port" "$path"
		return
	fi
	printf 'http://%s:%s%s' "$address" "$port" "$path"
}

host_http_request() {
	local address="$1"
	local port="$2"
	local path="$3"
	local body="$4"
	local output="$5"
	rm -f "$output"
	curl --fail --silent --show-error --noproxy '*' --globoff \
		--connect-timeout 2 --max-time 8 \
		"$(http_url "$address" "$port" "$path")" --output "$output" \
		|| return 1
	[[ -f "$output" ]] && [[ "$(<"$output")" == "$body" ]]
}

service_listener_exists() {
	local protocol="$1"
	local port="$2"
	local args=(-H -ln)
	case "$protocol" in
	tcp) args+=(-t) ;;
	udp) args+=(-u) ;;
	*) return 2 ;;
	esac
	ip netns exec "$peer_ns" ss "${args[@]}" "sport = :$port" \
		| grep -q .
}

start_udp_echo_in_netns() {
	local address="$1"
	local port="$2"
	local access_log="$3"
	rm -f "$access_log"
	ip netns exec "$peer_ns" python3 -u -c '
import socket
import sys

address = sys.argv[1]
port = int(sys.argv[2])
access_log = sys.argv[3]
family = socket.AF_INET6 if ":" in address else socket.AF_INET
server = socket.socket(family, socket.SOCK_DGRAM)
server.bind((address, port))
seen = set()
while True:
    data, peer = server.recvfrom(65535)
    with open(access_log, "ab") as output:
        output.write(data + b"\n")
    # Make every request prove that its client tolerates one lost datagram.
    if data not in seen:
        seen.add(data)
        continue
    server.sendto(b"pong:" + data, peer)
' "$address" "$port" "$access_log" \
		>/dev/null 2>"$access_log.stderr" &
	echo "$!"
}

host_udp_request() {
	local address="$1"
	local port="$2"
	local token="$3"
	python3 -c '
import socket
import sys
import time

address = sys.argv[1]
port = int(sys.argv[2])
token = sys.argv[3].encode()
family = socket.AF_INET6 if ":" in address else socket.AF_INET
client = socket.socket(family, socket.SOCK_DGRAM)
client.connect((address, port))
deadline = time.monotonic() + 5
while True:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise SystemExit("UDP echo response timed out")
    client.settimeout(min(0.25, remaining))
    client.send(token)
    try:
        response = client.recv(65535)
    except TimeoutError:
        continue
    if response != b"pong:" + token:
        raise SystemExit(f"unexpected UDP response: {response!r}")
    break
' "$address" "$port" "$token"
}

assert_complete_source_policy() {
	local tun_name="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "IPv4 default preferred source" \
		route_lookup_matches -4 "$default_v4_remote" "$tun_name" "$default_v4_local" \
		|| fail "IPv4 default route has the wrong preferred source"
	retry "$ALMAGEST_E2E_TIMEOUT" "IPv4 overlay preferred source" \
		route_lookup_matches -4 "$overlay_v4_remote" "$tun_name" "$overlay_v4_local" \
		|| fail "IPv4 overlay route has the wrong preferred source"
	retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 default preferred source" \
		route_lookup_matches -6 "$default_v6_remote" "$tun_name" "$default_v6_local" \
		|| fail "IPv6 default route has the wrong preferred source"
	retry "$ALMAGEST_E2E_TIMEOUT" "IPv6 overlay preferred source" \
		route_lookup_matches -6 "$overlay_v6_remote" "$tun_name" "$overlay_v6_local" \
		|| fail "IPv6 overlay route has the wrong preferred source"
	vpn_source_route_exists -4 default "$tun_name" "$default_v4_local" \
		|| fail "IPv4 VPN-table default source route is missing"
	vpn_source_route_exists -4 100.64.0.0/10 "$tun_name" "$overlay_v4_local" \
		|| fail "IPv4 VPN-table overlay source route is missing"
	vpn_source_route_exists -6 default "$tun_name" "$default_v6_local" \
		|| fail "IPv6 VPN-table default source route is missing"
	vpn_source_route_exists -6 fd7a:115c:a1e0::/48 "$tun_name" "$overlay_v6_local" \
		|| fail "IPv6 VPN-table overlay source route is missing"
}

assert_all_host_traffic() {
	local tag="$1"
	retry 90 "$tag default IPv4 HTTP" \
		host_http_request "$default_v4_remote" 18101 "/$tag" \
		"default IPv4 response" "$E2E_RUNTIME/$tag-default-v4-http.txt" \
		|| fail "$tag default IPv4 HTTP failed"
	retry 90 "$tag overlay IPv4 HTTP" \
		host_http_request "$overlay_v4_remote" 18103 "/$tag" \
		"overlay IPv4 response" "$E2E_RUNTIME/$tag-overlay-v4-http.txt" \
		|| fail "$tag overlay IPv4 HTTP failed"
	retry 90 "$tag default IPv6 HTTP" \
		host_http_request "$default_v6_remote" 18102 "/$tag" \
		"default IPv6 response" "$E2E_RUNTIME/$tag-default-v6-http.txt" \
		|| fail "$tag default IPv6 HTTP failed"
	retry 90 "$tag overlay IPv6 HTTP" \
		host_http_request "$overlay_v6_remote" 18104 "/$tag" \
		"overlay IPv6 response" "$E2E_RUNTIME/$tag-overlay-v6-http.txt" \
		|| fail "$tag overlay IPv6 HTTP failed"
	retry 90 "$tag default IPv4 UDP" \
		host_udp_request "$default_v4_remote" 19101 "$tag-default-v4" \
		|| fail "$tag default IPv4 UDP failed"
	retry 90 "$tag overlay IPv4 UDP" \
		host_udp_request "$overlay_v4_remote" 19103 "$tag-overlay-v4" \
		|| fail "$tag overlay IPv4 UDP failed"
	retry 90 "$tag default IPv6 UDP" \
		host_udp_request "$default_v6_remote" 19102 "$tag-default-v6" \
		|| fail "$tag default IPv6 UDP failed"
	retry 90 "$tag overlay IPv6 UDP" \
		host_udp_request "$overlay_v6_remote" 19104 "$tag-overlay-v6" \
		|| fail "$tag overlay IPv6 UDP failed"
}

run_parallel_host_traffic() {
	local tag="$1"
	local pids=()
	host_http_request "$default_v4_remote" 18101 "/$tag" \
		"default IPv4 response" "$E2E_RUNTIME/$tag-default-v4-http.txt" &
	pids+=("$!")
	host_http_request "$overlay_v4_remote" 18103 "/$tag" \
		"overlay IPv4 response" "$E2E_RUNTIME/$tag-overlay-v4-http.txt" &
	pids+=("$!")
	host_http_request "$default_v6_remote" 18102 "/$tag" \
		"default IPv6 response" "$E2E_RUNTIME/$tag-default-v6-http.txt" &
	pids+=("$!")
	host_http_request "$overlay_v6_remote" 18104 "/$tag" \
		"overlay IPv6 response" "$E2E_RUNTIME/$tag-overlay-v6-http.txt" &
	pids+=("$!")
	host_udp_request "$default_v4_remote" 19101 "$tag-default-v4" &
	pids+=("$!")
	host_udp_request "$overlay_v4_remote" 19103 "$tag-overlay-v4" &
	pids+=("$!")
	host_udp_request "$default_v6_remote" 19102 "$tag-default-v6" &
	pids+=("$!")
	host_udp_request "$overlay_v6_remote" 19104 "$tag-overlay-v6" &
	pids+=("$!")

	local index
	for index in "${!pids[@]}"; do
		wait "${pids[$index]}" \
			|| fail "$tag parallel request $((index + 1)) failed"
	done
}

trap cleanup EXIT

service_pids+=("$(start_static_http_target_in_netns \
	"$peer_ns" "$default_v4_remote" 18101 \
	"default IPv4 response" "$E2E_RUNTIME/default-v4-http.log")")
service_pids+=("$(start_static_http_target_in_netns \
	"$peer_ns" "$default_v6_remote" 18102 \
	"default IPv6 response" "$E2E_RUNTIME/default-v6-http.log")")
service_pids+=("$(start_static_http_target_in_netns \
	"$peer_ns" "$overlay_v4_remote" 18103 \
	"overlay IPv4 response" "$E2E_RUNTIME/overlay-v4-http.log")")
service_pids+=("$(start_static_http_target_in_netns \
	"$peer_ns" "$overlay_v6_remote" 18104 \
	"overlay IPv6 response" "$E2E_RUNTIME/overlay-v6-http.log")")
service_pids+=("$(start_udp_echo_in_netns \
	"$default_v4_remote" 19101 "$E2E_RUNTIME/default-v4-udp.log")")
service_pids+=("$(start_udp_echo_in_netns \
	"$default_v6_remote" 19102 "$E2E_RUNTIME/default-v6-udp.log")")
service_pids+=("$(start_udp_echo_in_netns \
	"$overlay_v4_remote" 19103 "$E2E_RUNTIME/overlay-v4-udp.log")")
service_pids+=("$(start_udp_echo_in_netns \
	"$overlay_v6_remote" 19104 "$E2E_RUNTIME/overlay-v6-udp.log")")

for port in 18101 18102 18103 18104; do
	retry "$ALMAGEST_E2E_TIMEOUT" "TCP service on port $port" \
		service_listener_exists tcp "$port" \
		|| fail "TCP service on port $port did not start"
done
for port in 19101 19102 19103 19104; do
	retry "$ALMAGEST_E2E_TIMEOUT" "UDP service on port $port" \
		service_listener_exists udp "$port" \
		|| fail "UDP service on port $port did not start"
done

initial_default="$(ip -j -4 route show table main default | jq -cS)"
start_daemon

for name in \
	"DefaultTun entry" \
	"Original OutDns" \
	"Route TunSplitter" \
	"Default WireGuard" \
	"Default OutNetwork" \
	"Overlay WireGuard" \
	"Overlay OutNetwork"; do
	wait_graph_node "$name" || fail "$name node is missing"
done

default_tun="$(node_id_by_name "DefaultTun entry")"
splitter="$(node_id_by_name "Route TunSplitter")"
default_wireguard="$(node_id_by_name "Default WireGuard")"
overlay_wireguard="$(node_id_by_name "Overlay WireGuard")"

wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_node_label "$splitter" splitting || fail "TunSplitter did not start"
wait_node_label "$default_wireguard" up || fail "default WireGuard did not become up"
wait_node_label "$overlay_wireguard" up || fail "overlay WireGuard did not become up"

retry "$ALMAGEST_E2E_TIMEOUT" "native DefaultTun interface" \
	default_tun_name >"$E2E_RUNTIME/default-tun-name.txt" \
	|| fail "native DefaultTun interface was not created"
tun_name="$(<"$E2E_RUNTIME/default-tun-name.txt")"

for address in \
	"$default_v4_local/32" \
	"$overlay_v4_local/32" \
	"$default_v6_local/128" \
	"$overlay_v6_local/128"; do
	retry "$ALMAGEST_E2E_TIMEOUT" "effective DefaultTun address $address" \
		effective_field_contains "$default_tun" tunAddrs "$address" \
		|| fail "DefaultTun did not receive address $address"
done

ipv6_tun_address_is_ready "$tun_name" "$default_v6_local" \
	|| fail "default IPv6 TUN address is tentative or does not have NODAD"
ipv6_tun_address_is_ready "$tun_name" "$overlay_v6_local" \
	|| fail "overlay IPv6 TUN address is tentative or does not have NODAD"

main_routes_through_tun_are_absent "$tun_name" \
	|| fail "a DefaultTun route leaked into the main table"
marked_route_uses_physical_network 203.0.113.2 \
	|| fail "marked WireGuard transport does not bypass DefaultTun"
[[ "$(ip -j -4 route show table main default | jq -cS)" == "$initial_default" ]] \
	|| fail "DefaultTun changed the original main-table default"

assert_complete_source_policy "$tun_name"
assert_all_host_traffic initial
retry "$ALMAGEST_E2E_TIMEOUT" "default WireGuard handshake" \
	kernel_handshake_exists wgsr0 "$default_client_public" \
	|| fail "default kernel peer did not report a handshake"
retry "$ALMAGEST_E2E_TIMEOUT" "overlay WireGuard handshake" \
	kernel_handshake_exists wgsr1 "$overlay_client_public" \
	|| fail "overlay kernel peer did not report a handshake"
run_parallel_host_traffic parallel

# Remove the more-specific branch. Its source address and preferred-source
# routes must disappear while the default branch and native TUN stay usable.
overlay_link="$(link_id_by_port_names \
	"Route TunSplitter" "slot 2" "Overlay WireGuard" Tun)"
cli graph delete-link --id "$overlay_link" \
	>"$E2E_RUNTIME/delete-overlay-link.json"
retry "$ALMAGEST_E2E_TIMEOUT" "removed overlay IPv4 address" \
	effective_field_lacks "$default_tun" tunAddrs "$overlay_v4_local/32" \
	|| fail "removed overlay IPv4 address remains effective"
retry "$ALMAGEST_E2E_TIMEOUT" "removed overlay IPv6 address" \
	effective_field_lacks "$default_tun" tunAddrs "$overlay_v6_local/128" \
	|| fail "removed overlay IPv6 address remains effective"
retry "$ALMAGEST_E2E_TIMEOUT" "removed IPv4 source route" \
	route_lookup_matches -4 "$overlay_v4_remote" "$tun_name" "$default_v4_local" \
	|| fail "removed IPv4 source route remains active"
retry "$ALMAGEST_E2E_TIMEOUT" "removed IPv6 source route" \
	route_lookup_matches -6 "$overlay_v6_remote" "$tun_name" "$default_v6_local" \
	|| fail "removed IPv6 source route remains active"
ipv6_tun_address_is_ready "$tun_name" "$default_v6_local" \
	|| fail "retained IPv6 TUN address is tentative or does not have NODAD"
host_http_request "$default_v4_remote" 18101 /without-overlay \
	"default IPv4 response" "$E2E_RUNTIME/without-overlay-v4-http.txt" \
	|| fail "default IPv4 traffic stopped after overlay removal"
host_http_request "$default_v6_remote" 18102 /without-overlay \
	"default IPv6 response" "$E2E_RUNTIME/without-overlay-v6-http.txt" \
	|| fail "default IPv6 traffic stopped after overlay removal"
[[ "$(default_tun_name)" == "$tun_name" ]] \
	|| fail "source-route removal replaced the native TUN"

# Reattach the branch. This gives it a new link ID and changes merge order.
# Source selection must stay independent from address and link order.
add_link_by_port_name "$splitter" right "slot 2" \
	"$overlay_wireguard" left Tun \
	>"$E2E_RUNTIME/readd-overlay-link.json"
wait_node_label "$overlay_wireguard" up \
	|| fail "overlay WireGuard did not recover after reattachment"
retry "$ALMAGEST_E2E_TIMEOUT" "reattached overlay IPv4 address" \
	effective_field_contains "$default_tun" tunAddrs "$overlay_v4_local/32" \
	|| fail "reattached overlay IPv4 address is missing"
retry "$ALMAGEST_E2E_TIMEOUT" "reattached overlay IPv6 address" \
	effective_field_contains "$default_tun" tunAddrs "$overlay_v6_local/128" \
	|| fail "reattached overlay IPv6 address is missing"
ipv6_tun_address_is_ready "$tun_name" "$default_v6_local" \
	|| fail "default IPv6 TUN address is tentative or does not have NODAD after reattachment"
ipv6_tun_address_is_ready "$tun_name" "$overlay_v6_local" \
	|| fail "reattached IPv6 TUN address is tentative or does not have NODAD"
assert_complete_source_policy "$tun_name"
assert_all_host_traffic reattached
run_parallel_host_traffic reattached-parallel
[[ "$(default_tun_name)" == "$tun_name" ]] \
	|| fail "source-route reattachment replaced the native TUN"

assert_log_not_contains "almagest/DefaultTun" "configure default TUN failed"

stop_daemon
[[ "$(ip -j -4 route show table main default | jq -cS)" == "$initial_default" ]] \
	|| fail "daemon shutdown changed the original main-table default"

for token in \
	initial-default-v4 \
	initial-overlay-v4 \
	initial-default-v6 \
	initial-overlay-v6 \
	reattached-default-v4 \
	reattached-overlay-v4 \
	reattached-default-v6 \
	reattached-overlay-v6; do
	grep -F "$token" "$E2E_RUNTIME"/*-udp.log >/dev/null \
		|| fail "UDP service did not receive $token"
done

log "case passed"
