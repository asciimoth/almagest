#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

default_route_url="http://198.51.100.1:18080/default-route-probe"
default_route_body="ordinary default route"

cleanup() {
	stop_daemon
	if [[ -x "$E2E_RUNTIME/cleanup-tun.sh" ]]; then
		"$E2E_RUNTIME/cleanup-tun.sh"
	fi
}

curl_url() {
	local url="$1"
	local output="$2"
	curl --fail --silent --show-error --noproxy '*' \
		--connect-timeout 2 --max-time 5 \
		"$url" \
		--output "$output"
}

curl_url_fails() {
	local url="$1"
	if curl_url "$url" /tmp/e2e-tun-routing-unexpected-body.txt \
		>/tmp/e2e-tun-routing-unexpected-stdout.txt \
		2>/tmp/e2e-tun-routing-expected-failure.txt; then
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

delete_node() {
	local id="$1"
	cli graph delete-node --id "$id" >/tmp/e2e-tun-routing-delete-node.json
}

configure_tun() {
	local node="$1"
	local name="$2"
	local addr="$3"
	local route="$4"
	cli graph apply-menu --node "$node" --json \
		"{\"config\":{\"name\":\"$name\",\"tunAddrs\":\"$addr\",\"tunRoutes\":\"$route\",\"mtu\":0}}" \
		>/tmp/e2e-tun-routing-configure-tun.json
}

assert_default_route_blocked() {
	retry "$ALMAGEST_E2E_TIMEOUT" "$1" \
		curl_url_fails "$default_route_url" \
		|| fail "default-route probe unexpectedly bypassed disconnected DefaultTun"
}

assert_default_route_reachable() {
	retry "$ALMAGEST_E2E_TIMEOUT" "$1" \
		curl_url "$default_route_url" /tmp/e2e-tun-routing-default-route.txt \
		|| fail "default-route probe did not recover after DefaultTun removal"
	assert_body /tmp/e2e-tun-routing-default-route.txt "$default_route_body"
}

assert_server_1_reachable() {
	retry "$ALMAGEST_E2E_TIMEOUT" "$1" \
		curl_url http://10.7.160.2:8080/ /tmp/e2e-tun-routing-server-1.txt \
		|| fail "request to Server 1 failed"
	assert_body /tmp/e2e-tun-routing-server-1.txt "Server 1"
}

assert_server_1_blocked() {
	retry "$ALMAGEST_E2E_TIMEOUT" "$1" \
		curl_url_fails http://10.7.160.2:8080/ \
		|| fail "request to Server 1 unexpectedly succeeded"
}

assert_server_2_reachable() {
	retry "$ALMAGEST_E2E_TIMEOUT" "$1" \
		curl_url http://10.210.148.2:8080/ /tmp/e2e-tun-routing-server-2.txt \
		|| fail "request to Server 2 failed"
	assert_body /tmp/e2e-tun-routing-server-2.txt "Server 2"
}

assert_server_2_blocked() {
	retry "$ALMAGEST_E2E_TIMEOUT" "$1" \
		curl_url_fails http://10.210.148.2:8080/ \
		|| fail "request to Server 2 unexpectedly succeeded"
}

trap cleanup EXIT

start_daemon

wait_node_class_available "almagest/Tun" \
	|| fail "Tun node class is unavailable"
wait_node_class_available "almagest/DefaultTun" \
	|| fail "DefaultTun node class is unavailable"
wait_node_class_available "almagest/Spoofer" \
	|| fail "Spoofer node class is unavailable"
wait_node_class_available "almagest/MockNetwork" \
	|| fail "MockNetwork node class is unavailable"
wait_node_class_available "almagest/HttpServer" \
	|| fail "HttpServer node class is unavailable"

wait_graph_node "Tun 1504" || fail "Tun 1504 node missing"
wait_graph_node "Tun 1508" || fail "Tun 1508 node missing"
wait_graph_node "DefaultTun 1543" || fail "DefaultTun node missing"
wait_graph_node "OutDns 1542" || fail "OutDns node missing"
wait_graph_node "Spoofer 1510" || fail "Spoofer 1510 node missing"
wait_graph_node "Spoofer 1513" || fail "Spoofer 1513 node missing"

tun_one="$(node_id_by_name "Tun 1504")"
tun_two="$(node_id_by_name "Tun 1508")"
default_tun="$(node_id_by_name "DefaultTun 1543")"
out_dns="$(node_id_by_name "OutDns 1542")"
spoofer_one="$(node_id_by_name "Spoofer 1510")"
spoofer_two="$(node_id_by_name "Spoofer 1513")"

wait_node_label "$tun_one" tun1 || fail "first Tun did not become ready as tun1"
wait_node_label "$tun_two" tun2 || fail "second Tun did not become ready as tun2"
wait_node_label "$default_tun" ready || fail "DefaultTun did not become ready"
wait_node_label "$spoofer_one" forwarding || fail "first Spoofer did not start forwarding"
wait_node_label "$spoofer_two" forwarding || fail "second Spoofer did not start forwarding"

wait_log_contains "almagest/HttpServer" "listening on 10.7.160.2:8080" \
	|| fail "Server 1 did not start listening"
wait_log_contains "almagest/HttpServer" "listening on 10.210.148.2:8080" \
	|| fail "Server 2 did not start listening"

assert_default_route_blocked "ordinary default route blocked by disconnected DefaultTun"
assert_server_1_reachable "curl reaches Server 1 through initial tun1"
assert_server_2_reachable "curl reaches Server 2 through initial tun2"

delete_node "$tun_one"
wait_node_label "$spoofer_one" "waiting for tun" \
	|| fail "first Spoofer did not notice removed Tun"
assert_server_1_blocked "Server 1 blocked after removing tun1"
assert_server_2_reachable "Server 2 still reachable after removing tun1"
assert_default_route_blocked "ordinary default route still blocked after removing tun1"

delete_node "$default_tun"
assert_server_1_blocked "Server 1 still blocked after removing DefaultTun"
assert_server_2_reachable "Server 2 unaffected after removing DefaultTun"
assert_default_route_reachable "ordinary default route restored after removing DefaultTun"

tun_one="$(add_node "almagest/Tun" "Tun dynamic 1")"
configure_tun "$tun_one" tun1 "10.7.160.1/24" "10.7.160.0/24"
wait_node_label "$tun_one" tun1 || fail "re-added first Tun did not become ready as tun1"
add_link_by_port_name "$tun_one" right Tun "$spoofer_one" left Tun \
	>/tmp/e2e-tun-routing-readd-tun1-link.json
wait_node_label "$spoofer_one" forwarding \
	|| fail "first Spoofer did not resume forwarding after re-added Tun"
assert_server_1_reachable "Server 1 restored after re-adding tun1"
assert_server_2_reachable "Server 2 still reachable after re-adding tun1"
assert_default_route_reachable "ordinary default route remains reachable after re-adding tun1"

delete_node "$tun_two"
wait_node_label "$spoofer_two" "waiting for tun" \
	|| fail "second Spoofer did not notice removed Tun"
assert_server_1_reachable "Server 1 still reachable after removing tun2"
assert_server_2_blocked "Server 2 blocked after removing tun2"
assert_default_route_reachable "ordinary default route remains reachable after removing tun2"

tun_two="$(add_node "almagest/Tun" "Tun dynamic 2")"
configure_tun "$tun_two" tun2 "10.210.148.1/24" "10.210.148.0/24"
wait_node_label "$tun_two" tun2 || fail "re-added second Tun did not become ready as tun2"
add_link_by_port_name "$tun_two" right Tun "$spoofer_two" left Tun \
	>/tmp/e2e-tun-routing-readd-tun2-link.json
wait_node_label "$spoofer_two" forwarding \
	|| fail "second Spoofer did not resume forwarding after re-added Tun"
assert_server_1_reachable "Server 1 still reachable after re-adding tun2"
assert_server_2_reachable "Server 2 restored after re-adding tun2"
assert_default_route_reachable "ordinary default route remains reachable after re-adding tun2"

default_tun="$(add_node "almagest/DefaultTun" "DefaultTun dynamic 1")"
wait_node_label "$default_tun" ready || fail "re-added DefaultTun did not become ready"
add_link_by_port_name "$out_dns" left DNS "$default_tun" right DNS \
	>/tmp/e2e-tun-routing-dynamic-1-link-dns.json
assert_server_1_reachable "Server 1 unaffected after re-adding DefaultTun"
assert_server_2_reachable "Server 2 unaffected after re-adding DefaultTun"
assert_default_route_blocked "ordinary default route blocked again after re-adding disconnected DefaultTun"

delete_node "$default_tun"
assert_server_1_reachable "Server 1 unaffected after removing dynamic DefaultTun"
assert_server_2_reachable "Server 2 unaffected after removing dynamic DefaultTun"
assert_default_route_reachable "ordinary default route restored after removing dynamic DefaultTun"

default_tun="$(add_node "almagest/DefaultTun" "DefaultTun dynamic 2")"
wait_node_label "$default_tun" ready || fail "second re-added DefaultTun did not become ready"
add_link_by_port_name "$out_dns" left DNS "$default_tun" right DNS \
	>/tmp/e2e-tun-routing-dynamic-2-link-dns.json
assert_server_1_reachable "Server 1 unaffected after second DefaultTun re-add"
assert_server_2_reachable "Server 2 unaffected after second DefaultTun re-add"
assert_default_route_blocked "ordinary default route blocked after second disconnected DefaultTun re-add"

wait_log_contains "almagest/HttpServer" "GET /" \
	|| fail "HTTP servers did not log GET requests"

log "case passed"
