#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

proxied_body_is() {
	local host="$1"
	local expected="$2"
	local out="/tmp/e2e-network-router-${host}.txt"

	curl -g --fail --silent --show-error --max-time 2 \
		--socks5-hostname 127.0.0.1:1081 \
		"http://$host/" \
		--output "$out" \
		2>/dev/null \
		&& [[ "$(<"$out")" == "$expected" ]]
}

assert_proxied_body() {
	local host="$1"
	local expected="$2"

	retry "$ALMAGEST_E2E_TIMEOUT" "proxied HTTP request to $host returns $expected" \
		proxied_body_is "$host" "$expected" \
		|| fail "proxied request to $host did not return $expected"
}

proxied_request_fails() {
	local host="$1"
	! curl -g --fail --silent --show-error --max-time 1 \
		--socks5-hostname 127.0.0.1:1081 \
		"http://$host/" \
		--output /dev/null \
		2>/dev/null
}

assert_proxied_failure() {
	local host="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "proxied HTTP request to $host fails" \
		proxied_request_fails "$host" \
		|| fail "proxied request to $host stayed usable"
}

apply_router_rules() {
	local rules="$1"
	jq -n --arg rules "$rules" '{"config":{"rules":$rules}}' \
		| cli graph apply-menu --name "NetworkRouter 1686" >/dev/null
}

port_exists() {
	local node="$1"
	local direction="$2"
	local name="$3"
	local id
	id="$(node_id_by_name "$node")" || return 1
	node_port_id "$id" "$direction" "$name" >/dev/null
}

port_missing() {
	! port_exists "$1" "$2" "$3"
}

start_daemon

wait_graph_node "LocalProxy 1684" || fail "LocalProxy node missing"
wait_graph_node "NetworkRouter 1686" || fail "NetworkRouter node missing"
wait_graph_node "MockNetwork 1692" || fail "slot 1 MockNetwork node missing"
wait_graph_node "MockNetwork 1694" || fail "slot 2 MockNetwork node missing"
wait_graph_node "MockNetwork 1696" || fail "slot 3 MockNetwork node missing"
wait_graph_node "HttpServer 1701" || fail "Server 1 HTTP node missing"
wait_graph_node "HttpServer 1710" || fail "Server 2 HTTP node missing"
wait_graph_node "HttpServer 1719" || fail "Server 3 HTTP node missing"

wait_log_contains "almagest/LocalProxy" "socks listening on 127.0.0.1:1081" \
	|| fail "local proxy did not start listening"

assert_proxied_body "host1" "Server 1"
assert_proxied_body "host2" "Server 2"
assert_proxied_body "not-host1-or-host2" "Server 3"

wait_log_contains "almagest/LocalProxy" "connect host1:80" \
	|| fail "local proxy did not log the host1 connect request"
wait_log_contains "almagest/LocalProxy" "connect host2:80" \
	|| fail "local proxy did not log the host2 connect request"
wait_log_contains "almagest/LocalProxy" "connect not-host1-or-host2:80" \
	|| fail "local proxy did not log the default route connect request"

router="$(node_id_by_name "NetworkRouter 1686")"
proxy="$(node_id_by_name "LocalProxy 1684")"
mock_one="$(node_id_by_name "MockNetwork 1692")"
mock_three="$(node_id_by_name "MockNetwork 1696")"

# Repeated live rule replacement must update the stable router without
# replacing or closing its consumer wrapper.
for mapping in swapped initial swapped initial; do
	if [[ "$mapping" == "swapped" ]]; then
		apply_router_rules $'ADDR_S host1\nSLOT 2\n\nADDR_S host2\nSLOT 1\n\nTRUE\nSLOT 3'
		assert_proxied_body host1 "Server 2"
		assert_proxied_body host2 "Server 1"
	else
		apply_router_rules $'ADDR_S host1\nSLOT 1\n\nADDR_S host2\nSLOT 2\n\nTRUE\nSLOT 3'
		assert_proxied_body host1 "Server 1"
		assert_proxied_body host2 "Server 2"
	fi
	assert_proxied_body not-host1-or-host2 "Server 3"
done

# Invalid text must leave the last usable routing program active.
apply_router_rules $'THIS IS NOT A ROUTER RULE'
assert_proxied_body host1 "Server 1"
assert_proxied_body host2 "Server 2"
assert_proxied_body not-host1-or-host2 "Server 3"

# Move the host1 route to a newly created slot. Remove the obsolete backend
# links and verify that the data path no longer depends on them.
apply_router_rules $'ADDR_S host1\nSLOT 4\n\nTRUE\nSLOT 3'
retry "$ALMAGEST_E2E_TIMEOUT" "NetworkRouter slot 4 port" \
	port_exists "NetworkRouter 1686" right "slot 4" \
	|| fail "NetworkRouter did not create slot 4"
port_exists "NetworkRouter 1686" right "slot 1" \
	|| fail "NetworkRouter removed linked obsolete slot 1"
port_exists "NetworkRouter 1686" right "slot 2" \
	|| fail "NetworkRouter removed linked obsolete slot 2"
add_link_by_port_name "$router" right "slot 4" "$mock_one" left Network >/dev/null
assert_proxied_body host1 "Server 1"
assert_proxied_body host2 "Server 3"

slot_one_link="$(link_id_by_port_names "NetworkRouter 1686" "slot 1" "MockNetwork 1692" Network)"
slot_two_link="$(link_id_by_port_names "NetworkRouter 1686" "slot 2" "MockNetwork 1694" Network)"
cli graph delete-link --id "$slot_one_link" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "unused NetworkRouter slot 1 removed immediately" \
	port_missing "NetworkRouter 1686" right "slot 1" \
	|| fail "NetworkRouter kept unused slot 1 after unlink"
port_exists "NetworkRouter 1686" right "slot 2" \
	|| fail "NetworkRouter removed still-linked obsolete slot 2"
cli graph delete-link --id "$slot_two_link" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "unused NetworkRouter slot 2 removed immediately" \
	port_missing "NetworkRouter 1686" right "slot 2" \
	|| fail "NetworkRouter kept unused slot 2 after unlink"
assert_proxied_body host1 "Server 1"
assert_proxied_body host2 "Server 3"

# Detaching one active backend must only break routes that select that slot.
slot_three_link="$(link_id_by_port_names "NetworkRouter 1686" "slot 3" "MockNetwork 1696" Network)"
cli graph delete-link --id "$slot_three_link" >/dev/null
assert_proxied_body host1 "Server 1"
assert_proxied_failure host2
add_link_by_port_name "$router" right "slot 3" "$mock_three" left Network >/dev/null
assert_proxied_body host2 "Server 3"

# A detached consumer must discard its wrapper. The first request after the
# same graph edge is restored must use a fresh, current wrapper.
proxy_link="$(link_id_by_port_names "LocalProxy 1684" proxy "NetworkRouter 1686" Network)"
cli graph delete-link --id "$proxy_link" >/dev/null
assert_proxied_failure host1
add_link_by_port_name "$proxy" right proxy "$router" left Network >/dev/null
assert_proxied_body host1 "Server 1"
assert_proxied_body host2 "Server 3"

# The live rule and link changes must survive a daemon restart.
stop_daemon
start_daemon
retry "$ALMAGEST_E2E_TIMEOUT" "unused NetworkRouter slot 1 absent after restore" \
	port_missing "NetworkRouter 1686" right "slot 1" \
	|| fail "NetworkRouter restored unused slot 1"
retry "$ALMAGEST_E2E_TIMEOUT" "unused NetworkRouter slot 2 absent after restore" \
	port_missing "NetworkRouter 1686" right "slot 2" \
	|| fail "NetworkRouter restored unused slot 2"
assert_proxied_body host1 "Server 1"
assert_proxied_body host2 "Server 3"

log "case passed"
