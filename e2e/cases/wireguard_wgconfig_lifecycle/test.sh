#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

menu_value() {
	local node="$1"
	local block="$2"
	local field="$3"
	cli graph menu --id "$node" | jq -er \
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

wgconfig_status_is() {
	local node="$1"
	local state="$2"
	local runtime_peers="$3"
	[[ "$(menu_value "$node" status state)" == "$state" \
		&& "$(menu_value "$node" status runtimePeers)" == "$runtime_peers" ]]
}

wait_wgconfig_status() {
	local node="$1"
	local state="$2"
	local runtime_peers="$3"
	local description="$4"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		wgconfig_status_is "$node" "$state" "$runtime_peers" \
		|| fail "$description did not converge"
}

http_request_count() {
	cli log subscribe | jq -er '
		[
			.entries[]
			| select(
				.node_class == "almagest/HttpServer"
				and (.message | contains("GET /lifecycle"))
			)
		]
		| length
	'
}

http_success_after_count() {
	local node="$1"
	local previous_count="$2"
	local current_count
	current_count="$(http_request_count)"
	[[ "$current_count" -gt "$previous_count" \
		&& "$(menu_value "$node" result status)" == "Status: 200 OK" \
		&& -z "$(menu_value "$node" result error)" \
		&& "$(menu_value "$node" result response)" == "wgconfig-lifecycle-response" ]]
}

assert_http_success_once() {
	local node="$1"
	local description="$2"
	local previous_count
	previous_count="$(http_request_count)"
	cli graph click-menu-button --id "$node" --block request --button send >/dev/null
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" \
		http_success_after_count "$node" "$previous_count" \
		|| fail "$description did not return the expected response"
}

apply_client_peer() {
	local public_key="$1"
	jq -cn --arg public_key "$public_key" '{
		peers: {
			peers: [{
				id: "server",
				template: "peer",
				values: {
					publicKey: $public_key,
					presharedKeySecret: "",
					endpoint: "127.0.0.1:51871",
					allowedIPs: "10.207.20.1/32",
					persistentKeepaliveInterval: 1,
					amneziaOverride: false,
					amneziaWGEnabled: false
				}
			}]
		}
	}' | cli graph apply-menu --name "Client Config" >/dev/null
}

start_daemon

for name in \
	"WireGuard Client" \
	"Client Config" \
	"VTun Client" \
	"HTTP Client" \
	"WireGuard Server" \
	"Server Config" \
	"VTun Server" \
	"HTTP Server" \
	"Host Network"; do
	wait_graph_node "$name" || fail "$name node missing"
done

client_wg="$(node_id_by_name "WireGuard Client")"
client_config="$(node_id_by_name "Client Config")"
client_vtun="$(node_id_by_name "VTun Client")"
http_client="$(node_id_by_name "HTTP Client")"
server_wg="$(node_id_by_name "WireGuard Server")"
server_config="$(node_id_by_name "Server Config")"
server_vtun="$(node_id_by_name "VTun Server")"
host_network="$(node_id_by_name "Host Network")"

wait_node_label "$client_wg" up || fail "client WireGuard did not become up"
wait_node_label "$server_wg" up || fail "server WireGuard did not become up"
wait_node_label "$client_vtun" wgconfig-client || fail "client VTun did not become ready"
wait_node_label "$server_vtun" wgconfig-server || fail "server VTun did not become ready"
wait_wgconfig_status "$client_config" up 1 "client WGConfig startup"
wait_wgconfig_status "$server_config" up 1 "server WGConfig startup"
assert_http_success_once "$http_client" "initial WGConfig HTTP request"

# A rejected form must preserve the last valid desired and runtime peer.
apply_client_peer invalid-public-key
wait_wgconfig_status "$client_config" error 1 "invalid peer form rejection"
[[ -n "$(menu_value "$client_config" status error)" ]] \
	|| fail "invalid peer form did not expose an error"
assert_http_success_once "$http_client" "request after invalid peer form"
apply_client_peer "IJ7Kp5Sm2Wiaj0J8/mfx5A/X79XmohmIhHjMpNJVZTQ="
wait_wgconfig_status "$client_config" up 1 "valid peer form recovery"
assert_http_success_once "$http_client" "first request after valid peer form"

# Detaching the transport network must retain the peer but remove its usable
# endpoint. Reattachment must install a fresh transport.
client_network_link="$(link_id_by_port_names "Client Config" Network "Host Network" Network)"
cli graph delete-link --id "$client_network_link" >/dev/null
wait_wgconfig_status "$client_config" "network detached; peer traffic is dropped" 1 \
	"client WGConfig network detach"
add_link_by_port_name "$client_config" right Network "$host_network" left Network >/dev/null
wait_wgconfig_status "$client_config" up 1 "client WGConfig network reattach"
assert_http_success_once "$http_client" "first request after WGConfig network reattach"

# Detaching the controller must close the detached API and remove all runtime
# state owned by this WGConfig node.
client_controller_link="$(link_id_by_port_names "Client Config" WireGuard "WireGuard Client" WGCtrl)"
cli graph delete-link --id "$client_controller_link" >/dev/null
wait_wgconfig_status "$client_config" "waiting for WireGuard" 0 \
	"client WGConfig controller detach"
add_link_by_port_name "$client_config" right WireGuard "$client_wg" left WGCtrl >/dev/null
wait_wgconfig_status "$client_config" up 1 "client WGConfig controller reattach"
assert_http_success_once "$http_client" "first request after WGConfig controller reattach"

# Exercise stale async completions. Restore both links without waiting for the
# detach work, and vary which replacement arrives first.
for iteration in 1 2 3; do
	client_controller_link="$(link_id_by_port_names "Client Config" WireGuard "WireGuard Client" WGCtrl)"
	client_network_link="$(link_id_by_port_names "Client Config" Network "Host Network" Network)"
	cli graph delete-link --id "$client_controller_link" >/dev/null
	cli graph delete-link --id "$client_network_link" >/dev/null
	if ((iteration % 2 == 1)); then
		add_link_by_port_name "$client_config" right Network "$host_network" left Network >/dev/null
		add_link_by_port_name "$client_config" right WireGuard "$client_wg" left WGCtrl >/dev/null
	else
		add_link_by_port_name "$client_config" right WireGuard "$client_wg" left WGCtrl >/dev/null
		add_link_by_port_name "$client_config" right Network "$host_network" left Network >/dev/null
	fi
	wait_wgconfig_status "$client_config" up 1 \
		"client WGConfig rapid link cycle $iteration"
	assert_http_success_once "$http_client" \
		"first request after rapid WGConfig link cycle $iteration"
done

# Autosaved peer and link state must reconstruct both controller runtimes.
stop_daemon
start_daemon
client_config="$(node_id_by_name "Client Config")"
server_config="$(node_id_by_name "Server Config")"
http_client="$(node_id_by_name "HTTP Client")"
wait_wgconfig_status "$client_config" up 1 "client WGConfig after restart"
wait_wgconfig_status "$server_config" up 1 "server WGConfig after restart"
assert_http_success_once "$http_client" "first WGConfig request after restart"

log "case passed"
