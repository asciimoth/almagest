#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

menu_value() {
	local node="$1"
	local block="$2"
	local item="$3"
	cli graph menu --name "$node" | jq -er \
		--arg block "$block" \
		--arg item "$item" \
		'
		.blocks[]
		| select(.id == $block)
		| .items[]
		| select(.id == $item)
		| (.value // .text // "")
		'
}

udp_success_is() {
	local payload="$1"
	[[ "$(menu_value "UDP Client" result status)" == "Status: ok" \
		&& -z "$(menu_value "UDP Client" result error)" \
		&& "$(menu_value "UDP Client" result response)" == "pong:$payload" ]]
}

udp_failure_is() {
	[[ "$(menu_value "UDP Client" result status)" == "Status: fail" \
		&& -n "$(menu_value "UDP Client" result error)" ]]
}

send_udp() {
	cli graph click-menu-button \
		--name "UDP Client" \
		--block config \
		--button send \
		>/dev/null
}

assert_udp_success_once() {
	local payload="$1"
	local description="$2"
	send_udp
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" udp_success_is "$payload" \
		|| fail "$description did not return pong:$payload"
}

assert_udp_failure_once() {
	local description="$1"
	send_udp
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" udp_failure_is \
		|| fail "$description did not report a UDP failure"
}

apply_server_config() {
	local mode="$1"
	local local_port="$2"
	local remote_port="$3"
	local desired
	desired="$(jq -nc \
		--arg mode "$mode" \
		--argjson local_port "$local_port" \
		--argjson remote_port "$remote_port" \
		'{config: {
			network: "udp4",
			socket_mode: $mode,
			local_host: "127.0.0.1",
			local_port: $local_port,
			remote_host: "127.0.0.1",
			remote_port: $remote_port
		}}')"
	cli graph apply-menu --name "UDP Server" --json "$desired" >/dev/null
}

apply_client_config() {
	local mode="$1"
	local local_port="$2"
	local remote_port="$3"
	local payload="$4"
	local timeout_ms="$5"
	local desired
	desired="$(jq -nc \
		--arg mode "$mode" \
		--argjson local_port "$local_port" \
		--argjson remote_port "$remote_port" \
		--arg payload "$payload" \
		--argjson timeout_ms "$timeout_ms" \
		'{config: {
			network: "udp4",
			socket_mode: $mode,
			local_host: "127.0.0.1",
			local_port: $local_port,
			remote_host: "127.0.0.1",
			remote_port: $remote_port,
			payload: $payload,
			timeout_ms: $timeout_ms
		}}')"
	cli graph apply-menu --name "UDP Client" --json "$desired" >/dev/null
}

debug_log_count() {
	local class="$1"
	local text="$2"
	cli log subscribe | jq -er \
		--arg class "$class" \
		--arg text "$text" \
		'[
			.entries[]
			| select(.node_class == $class and (.message | contains($text)))
		] | length'
}

debug_log_count_greater_than() {
	local class="$1"
	local text="$2"
	local previous="$3"
	(( $(debug_log_count "$class" "$text") > previous ))
}

wait_new_server_listener() {
	local previous="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "replacement UDP server listener" \
		debug_log_count_greater_than \
		"almagest/UDPPingServer" "udp server listening on" "$previous"
}

start_daemon

wait_graph_node "UDP Network" || fail "UDP Network node is missing"
wait_graph_node "UDP Log" || fail "UDP Log node is missing"
wait_graph_node "UDP Server" || fail "UDP Server node is missing"
wait_graph_node "UDP Client" || fail "UDP Client node is missing"
network_id="$(node_id_by_name "UDP Network")"
log_id="$(node_id_by_name "UDP Log")"
server_id="$(node_id_by_name "UDP Server")"
client_id="$(node_id_by_name "UDP Client")"
wait_log_contains "almagest/UDPPingServer" "udp server listening on" \
	|| fail "initial UDP server did not listen"

for request in 1 2 3 4 5; do
	assert_udp_success_once "initial-ping" "initial UDP request $request"
done

# A removed client link must invalidate its network wrapper. Restoring the
# same graph edge must provide a usable replacement for the first request.
client_link="$(link_id_by_port_names "UDP Client" Network "UDP Log" Network)"
cli graph delete-link --id "$client_link" >/dev/null
assert_udp_failure_once "request with detached client network"
add_link_by_port_name "$client_id" right Network "$log_id" left Network >/dev/null
assert_udp_success_once "initial-ping" "first request after client reattach"

# A server-side detach closes its listener. A new link must reconstruct the
# listener instead of retaining the failed attachment.
server_link="$(link_id_by_port_names "UDP Server" Network "UDP Log" Network)"
listener_count="$(debug_log_count "almagest/UDPPingServer" "udp server listening on")"
cli graph delete-link --id "$server_link" >/dev/null
assert_udp_failure_once "request with detached UDP server"
add_link_by_port_name "$server_id" right Network "$log_id" left Network >/dev/null
wait_new_server_listener "$listener_count" \
	|| fail "UDP server did not open a replacement listener"
assert_udp_success_once "initial-ping" "first request after server reattach"

# Replacing the LogNetwork input closes every emitted child wrapper. Both UDP
# nodes must accept the next generation and recover without graph rebuilds.
upstream_link="$(link_id_by_port_names "UDP Log" Network "UDP Network" Network)"
listener_count="$(debug_log_count "almagest/UDPPingServer" "udp server listening on")"
cli graph delete-link --id "$upstream_link" >/dev/null
assert_udp_failure_once "request with detached wrapper input"
add_link_by_port_name "$log_id" right Network "$network_id" left Network >/dev/null
wait_new_server_listener "$listener_count" \
	|| fail "UDP server did not recover after wrapper input replacement"
assert_udp_success_once "initial-ping" "first request after wrapper input replacement"

# Change both endpoints from listen-server/dial-client to
# dial-server/listen-client while also changing their fixed ports.
listener_count="$(debug_log_count "almagest/UDPPingServer" "udp server listening on")"
apply_server_config dial 19092 19093
apply_client_config listen 19093 19092 reverse-modes 500
wait_new_server_listener "$listener_count" \
	|| fail "UDP server did not restart after live socket-mode change"
assert_udp_success_once reverse-modes "request after live socket-mode change"

# Make the connected server accept traffic only from another peer port. The
# client send succeeds, but its response stays pending until link invalidation.
listener_count="$(debug_log_count "almagest/UDPPingServer" "udp server listening on")"
apply_server_config dial 19092 19094
wait_new_server_listener "$listener_count" \
	|| fail "UDP server did not apply the mismatched peer configuration"
apply_client_config listen 19093 19092 stale-request 2500
stale_failure_count="$(debug_log_count "almagest/UDPPingClient" "failed:")"
send_udp
wait_node_label "$client_id" "running..." \
	|| fail "stale UDP request did not start"
# A config generation change stops the old worker without closing its current
# UDP connection. Use another local port so the new generation can complete
# before the old request reaches its timeout.
apply_client_config listen 19095 19092 recovered-generation 500
listener_count="$(debug_log_count "almagest/UDPPingServer" "udp server listening on")"
apply_server_config dial 19092 19095
wait_new_server_listener "$listener_count" \
	|| fail "UDP server did not restore its current peer configuration"
assert_udp_success_once recovered-generation "current request after stale request invalidation"
[[ "$(debug_log_count "almagest/UDPPingClient" "failed:")" == "$stale_failure_count" ]] \
	|| fail "invalidated UDP request completed before the current request"
retry "$ALMAGEST_E2E_TIMEOUT" "invalidated UDP request completion" \
	debug_log_count_greater_than \
	"almagest/UDPPingClient" "failed:" "$stale_failure_count" \
	|| fail "invalidated UDP request did not reach a terminal worker path"
udp_success_is recovered-generation \
	|| fail "a stale UDP result replaced the current successful result"

# The form changes above must be persisted by the daemon and restored into a
# fresh graph with working runtime resources.
stop_daemon
start_daemon
wait_log_contains "almagest/UDPPingServer" "udp server listening on" \
	|| fail "restarted UDP server did not listen"
assert_udp_success_once recovered-generation "first request after daemon restart"

log "case passed"
