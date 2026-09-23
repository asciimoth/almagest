#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

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

http_success_is() {
	[[ "$(menu_text "Lifecycle Client" result status)" == "Status: 200 OK" \
		&& -z "$(menu_text "Lifecycle Client" result error)" \
		&& "$(menu_text "Lifecycle Client" result response)" == "tun-middleware-lifecycle-response" ]]
}

click_http_once() {
	cli graph click-menu-button \
		--name "Lifecycle Client" \
		--block request \
		--button send \
		>/dev/null
}

assert_http_success_once() {
	local description="$1"
	click_http_once
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" http_success_is \
		|| fail "$description did not return the expected response"
}

copy_waits_for_client() {
	node_label_is "$copy" "waiting for TunA"
}

wait_copying() {
	wait_node_label "$copy" copying \
		|| fail "TunCopy did not attach both current TUN wrappers"
}

wait_firewall_filtering() {
	wait_node_label "$client_firewall" "filtering 0 rules" \
		|| fail "TunFirewall did not attach the current TUN wrapper"
}

wait_firewall_and_copy_waiting() {
	wait_node_label "$client_firewall" "waiting for TUN" \
		|| fail "TunFirewall kept a closed input wrapper"
	retry "$ALMAGEST_E2E_TIMEOUT" "TunCopy detach after TunFirewall input failure" \
		copy_waits_for_client \
		|| fail "TunCopy kept the output from a failed TunFirewall"
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

wait_new_tun_close() {
	local previous="$1"
	retry "$ALMAGEST_E2E_TIMEOUT" "LogTun observes wrapper close" \
		debug_log_count_greater_than "almagest/LogTun" "tun.Close" "$previous" \
		|| fail "LogTun did not observe a closed TUN wrapper"
}

start_daemon

for name in \
	"Lifecycle Client" \
	"Client VTun" \
	"Client Stripper" \
	"Client LogTun" \
	"Client Firewall" \
	"Lifecycle Copy" \
	"Server LogTun" \
	"Server Stripper" \
	"Server VTun" \
	"Lifecycle Server" \
	"Observer VTun" \
	"Observer LogTun" \
	"Lifecycle Spoofer" \
	"Spoofer Mock"; do
	wait_graph_node "$name" || fail "$name node missing"
done

client_vtun="$(node_id_by_name "Client VTun")"
client_stripper="$(node_id_by_name "Client Stripper")"
client_log="$(node_id_by_name "Client LogTun")"
client_firewall="$(node_id_by_name "Client Firewall")"
copy="$(node_id_by_name "Lifecycle Copy")"
observer_vtun="$(node_id_by_name "Observer VTun")"
observer_log="$(node_id_by_name "Observer LogTun")"
spoofer="$(node_id_by_name "Lifecycle Spoofer")"

wait_node_label "$client_vtun" middleware-client \
	|| fail "client VTun did not become ready"
wait_node_label "$(node_id_by_name "Server VTun")" middleware-server \
	|| fail "server VTun did not become ready"
wait_node_label "$client_stripper" "stripping config" \
	|| fail "client TunConfigStripper did not become ready"
wait_node_label "$client_log" "tun log" \
	|| fail "client LogTun did not become ready"
wait_firewall_filtering
wait_node_label "$(node_id_by_name "Lifecycle Server")" "10.215.0.2:18140" \
	|| fail "HTTP server did not listen through the server VTun"
wait_copying
wait_node_label "$observer_vtun" middleware-observer \
	|| fail "observer VTun did not become ready"
wait_node_label "$observer_log" "tun log" \
	|| fail "observer LogTun did not become ready"
wait_node_label "$spoofer" forwarding \
	|| fail "Spoofer did not attach its initial TUN wrapper"
assert_http_success_once "initial request through TUN middleware"

# Removing the final middleware output must detach TunCopy immediately. A new
# link must publish a new wrapper that works on the first request.
firewall_output="$(link_id_by_port_names "Client Firewall" Tun "Lifecycle Copy" TunA)"
cli graph delete-link --id "$firewall_output" >/dev/null
retry "$ALMAGEST_E2E_TIMEOUT" "TunCopy client detach after TunFirewall output removal" \
	copy_waits_for_client \
	|| fail "TunCopy kept the removed TunFirewall wrapper"
add_link_by_port_name "$copy" left TunA "$client_firewall" right Tun >/dev/null
wait_copying
assert_http_success_once "first request after TunFirewall output restore"

# Removing the stripper output invalidates the wrapper held by LogTun, the
# firewall input, and the wrapper that the firewall published to TunCopy.
close_count="$(debug_log_count "almagest/LogTun" "tun.Close")"
stripper_output="$(link_id_by_port_names "Client Stripper" Tun "Client LogTun" in)"
cli graph delete-link --id "$stripper_output" >/dev/null
wait_new_tun_close "$close_count"
wait_firewall_and_copy_waiting
add_link_by_port_name "$client_log" left in "$client_stripper" right Tun >/dev/null
wait_firewall_filtering
wait_copying
assert_http_success_once "first request after stripper output restore"

# Removing the stripper input closes all wrappers derived from the old VTun.
# Restoring the same graph edge must request and publish the current VTun.
close_count="$(debug_log_count "almagest/LogTun" "tun.Close")"
stripper_input="$(link_id_by_port_names "Client VTun" Tun "Client Stripper" Tun)"
cli graph delete-link --id "$stripper_input" >/dev/null
wait_new_tun_close "$close_count"
wait_firewall_and_copy_waiting
add_link_by_port_name "$client_stripper" left Tun "$client_vtun" right Tun >/dev/null
wait_firewall_filtering
wait_copying
assert_http_success_once "first request after stripper input restore"

# Spoofer must also detect a wrapper close that arrives through an unchanged
# LogTun link. Restoring the upstream edge must create a new netstack runtime.
observer_input="$(link_id_by_port_names "Observer VTun" Tun "Observer LogTun" in)"
cli graph delete-link --id "$observer_input" >/dev/null
wait_node_label "$spoofer" "waiting for tun" \
	|| fail "Spoofer kept forwarding after its indirect TUN input closed"
add_link_by_port_name "$observer_log" left in "$observer_vtun" right Tun >/dev/null
wait_node_label "$spoofer" forwarding \
	|| fail "Spoofer did not recover with a replacement TUN wrapper"

# This live edit replaces the client VTun without changing graph links. The
# replacement must pass through both middlewares and become the current TunA.
cli graph apply-menu --name "Client VTun" --json \
	'{"config":{"name":"middleware-client-rebuilt","localAddrs":"10.215.0.3/24","dnsServers":"","mtu":1500,"endpointCh":1024,"eventCh":10,"noLoopbackAddr":false,"disableSACK":false}}' \
	>/dev/null
wait_node_label "$client_vtun" middleware-client-rebuilt \
	|| fail "client VTun did not apply its live replacement config"
wait_copying
assert_http_success_once "first request after live VTun replacement"

# The live form edit is saved by the daemon. A fresh graph must reconstruct
# both middleware chains around the new VTun configuration.
stop_daemon
start_daemon
client_vtun="$(node_id_by_name "Client VTun")"
copy="$(node_id_by_name "Lifecycle Copy")"
wait_node_label "$client_vtun" middleware-client-rebuilt \
	|| fail "restarted client VTun did not restore its live config"
client_firewall="$(node_id_by_name "Client Firewall")"
spoofer="$(node_id_by_name "Lifecycle Spoofer")"
wait_firewall_filtering
wait_copying
wait_node_label "$spoofer" forwarding \
	|| fail "restarted Spoofer did not restore its TUN runtime"
assert_http_success_once "first request after daemon restart"

log "case passed"
